package pilot

import (
	"bytes"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
)

/**
Label:
aliyun.log: /var/log/hello.log[:json][;/var/log/abc/def.log[:txt]]
*/

const LABEL_SERVICE_LOGS = "aliyun.logs."
const FLUENTD_CONF_HOME = "/etc/fluentd"

const LABEL_PROJECT = "com.docker.compose.project"
const LABEL_SERVICE = "com.docker.compose.service"

type Pilot struct {
	mutex        sync.Mutex
	tpl          *template.Template
	base         string
	dockerClient *client.Client
	reloadable   bool
}

func New(tplStr string, baseDir string) (*Pilot, error) {
	tpl, err := template.New("fluentd").Parse(tplStr)
	if err != nil {
		return nil, err
	}

	if os.Getenv("DOCKER_API_VERSION") == "" {
		os.Setenv("DOCKER_API_VERSION", "1.23")
	}
	client, err := client.NewEnvClient()

	if err != nil {
		return nil, err
	}

	return &Pilot{
		dockerClient: client,
		tpl:          tpl,
		base:         baseDir,
	}, nil
}

func (p *Pilot) watch() error {

	p.reloadable = false
	if err := p.processAllContainers(); err != nil {
		return err
	}
	StartFluentd()
	p.reloadable = true

	ctx := context.Background()
	filter := filters.NewArgs()
	filter.Add("type", "container")

	options := types.EventsOptions{
		Filters: filter,
	}
	msgs, errs := p.client().Events(ctx, options)
	for {
		select {
		case msg := <-msgs:
			if err := p.processEvent(msg); err != nil {
				log.Errorf("fail to process event: %v,  %v", msg, err)
			}
		case err := <-errs:
			log.Warnf("error: %v", err)
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			} else {
				msgs, errs = p.client().Events(ctx, options)
			}
		}
	}
}

type Source struct {
	Application string
	Service     string
	Container   string
}

type LogConfig struct {
	Name         string
	HostDir      string
	ContainerDir string
	Format       string
	File         string
	Tags         map[string]string
}

func (p *Pilot) cleanConfigs() error {
	confDir := fmt.Sprintf("%s/conf.d", FLUENTD_CONF_HOME)
	d, err := os.Open(confDir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return err
	}

	for _, name := range names {
		path := filepath.Join(confDir, name)
		stat, err := os.Stat(filepath.Join(confDir, name))
		if err != nil {
			return err
		}
		if stat.Mode().IsRegular() {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil

}

func (p *Pilot) processAllContainers() error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	opts := types.ContainerListOptions{All: true}
	containers, err := p.client().ContainerList(context.Background(), opts)
	if err != nil {
		return err
	}

	//clean config
	if err := p.cleanConfigs(); err != nil {
		return err
	}

	for _, c := range containers {
		containerJSON, err := p.client().ContainerInspect(context.Background(), c.ID)
		if err != nil {
			return err
		}
		if err = p.newContainer(containerJSON); err != nil {
			return err
		}
	}

	return nil
}

func (p *Pilot) newContainer(containerJSON types.ContainerJSON) error {
	id := containerJSON.ID
	jsonLogPath := containerJSON.LogPath
	mounts := containerJSON.Mounts
	labels := containerJSON.Config.Labels

	//logConfig.containerDir match types.mountPoint
	/**
	场景：
	1. 容器一个路径，中间有多级目录对应宿主机不同的目录
	2. containerdir对应的目录不是直接挂载的，挂载的是它上级的目录

	查找：从containerdir开始查找最近的一层挂载
	*/

	source := Source{
		Application: labels[LABEL_PROJECT],
		Service: labels[LABEL_SERVICE],
		Container: strings.TrimPrefix(containerJSON.Name, "/"),
	}

	logConfigs := p.getLogConfigs(jsonLogPath, mounts, labels)
	if len(logConfigs) == 0 {
		return nil
	}


	//pilot.findMounts(logConfigs, jsonLogPath, mounts)
	//生成配置
	fluentdConfig, err := p.render(id, source, logConfigs)
	if err != nil {
		return err
	}
	//TODO validate config before save
	log.Infof("Save %s to %s", fluentdConfig, p.pathOf(id))
	if err = ioutil.WriteFile(p.pathOf(id), []byte(fluentdConfig), os.FileMode(0644)); err != nil {
		return err
	}
	p.tryReload()
	return nil
}

func (p *Pilot) tryReload() {
	if p.reloadable {
		ReloadFluentd()
	}
}

func (p *Pilot) pathOf(container string) string {
	return fmt.Sprintf("%s/conf.d/%s.conf", FLUENTD_CONF_HOME, container)
}

func (p *Pilot) delContainer(id string) error {
	log.Infof("Try remove config %s", id)
	if err := os.Remove(p.pathOf(id)); err != nil {
		return err
	}
	p.tryReload()
	return nil
}

func (p *Pilot) client() *client.Client {
	return p.dockerClient
}

func (p *Pilot) processEvent(msg events.Message) error {
	containerId := msg.Actor.ID
	ctx := context.Background()
	switch msg.Action {
	case "start":
		log.Debug("Process container start event: %s", containerId)
		if p.exists(containerId) {
			log.Debug("%s is already exists.", containerId)
			return nil
		}
		containerJSON, err := p.client().ContainerInspect(ctx, containerId)
		if err != nil {
			return err
		}
		return p.newContainer(containerJSON)
	case "destroy":
		log.Debug("Process container destory event: %s", containerId)
		p.delContainer(containerId)
	}
	return nil
}

func (p *Pilot) hostDirOf(path string, mounts map[string]types.MountPoint) string {
	for {
		if point, ok := mounts[path]; ok {
			return point.Source
		}
		path = filepath.Base(path)
		if path == "/" {
			break
		}
	}
	return ""
}

func (p *Pilot) parseTags(tags string) (map[string]string, error) {
	tagMap := make(map[string]string)
	if tags == "" {
		return tagMap, nil
	}

	kvArray := strings.Split(tags, ",")
	for _, kv := range kvArray {
		arr := strings.Split(kv, "=")
		if len(arr) != 2 {
			return nil, fmt.Errorf("%s is not a valid k=v format", kv)
		}
		key := strings.TrimSpace(arr[0])
		value := strings.TrimSpace(arr[1])
		if key == "" || value == "" {
			return nil, fmt.Errorf("%s is not a valid k=v format", kv)
		}
		tagMap[key] = value
	}
	return tagMap, nil

}

func (p *Pilot) parseLogConfig(prefix string, jsonLogPath string, mounts map[string]types.MountPoint, labels map[string]string) (*LogConfig, error) {
	path := labels[prefix]

	if path == "" {
		return nil, fmt.Errorf("label %s is empty or not exist.", prefix)
	}

	format := labels[prefix + ".format"]
	if format == "" {
		format = "none"
	}

	tags := labels[prefix + ".tags"]
	tagMap, err := p.parseTags(tags)

	if err != nil {
		return nil, fmt.Errorf("parse tags in %s error: %v", prefix + ".tags", err)
	}

	if path == "stdout" {
		return &LogConfig{
			Name:    strings.Split(prefix, ".")[2],
			HostDir: filepath.Join(p.base, filepath.Dir(jsonLogPath)),
			Format:  "json",
			File:    filepath.Base(jsonLogPath),
			Tags:    tagMap,
		}, nil
	}

	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%s must be absolute path, in label %s", path, prefix)
	}
	containerDir := filepath.Dir(path)
	file := filepath.Base(path)
	if file == "" {
		return nil, fmt.Errorf("%s must be a file path, not directory, in label %s", path, prefix)
	}

	hostDir := p.hostDirOf(containerDir, mounts)
	if hostDir == "" {
		return nil, fmt.Errorf("%s is not mount on host, in label %s", path, prefix)
	}

	return &LogConfig{
		Name:         strings.Split(prefix, ".")[2],
		ContainerDir: containerDir,
		Format:       format,
		File:         file,
		Tags:         tagMap,
		HostDir:      filepath.Join(p.base, hostDir),
	}, nil
}

func (p *Pilot) getLogConfigs(jsonLogPath string, mounts []types.MountPoint, labels map[string]string) []LogConfig {
	var ret []LogConfig

	mountsMap := make(map[string]types.MountPoint)
	for _, mount := range mounts {
		mountsMap[mount.Destination] = mount
	}

	for k, _ := range labels {
		if strings.HasPrefix(k, LABEL_SERVICE_LOGS) && strings.Count(k, ".") == 2 {
			config, err := p.parseLogConfig(k, jsonLogPath, mountsMap, labels)
			if err != nil {
				log.Warnf("Parse log config error: %v", err)
				continue
			}

			ret = append(ret, *config)
		}
	}
	return ret
}

func (p *Pilot) exists(containId string) bool {
	if _, err := os.Stat(p.pathOf(containId)); os.IsNotExist(err) {
		return false
	}
	return true
}

func (p *Pilot) render(containerId string, source Source, configList []LogConfig) (string, error) {
	log.Infof("logs: %v", configList)
	var buf bytes.Buffer

	context := map[string]interface{}{
		"containerId": containerId,
		"configList":  configList,
		"source": source,
	}
	if err := p.tpl.Execute(&buf, context); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (p *Pilot) reload() error {
	log.Info("Reload fluentd")
	return ReloadFluentd()
}

func Run(tpl string, baseDir string) error {
	p, err := New(tpl, baseDir)
	if err != nil {
		panic(err)
	}
	return p.watch()
}
