package pilot

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Global variables for FluentdPiloter
const (
	FLUENTD_EXEC_CMD  = "/usr/bin/fluentd"
	FLUENTD_BASE_CONF = "/etc/fluentd"
	FLUENTD_CONF_DIR  = FLUENTD_BASE_CONF + "/conf.d"
	FLUENTD_CONF_FILE = FLUENTD_BASE_CONF + "/fluentd.conf"
	FLUENTD_PLUGINS   = FLUENTD_BASE_CONF + "/plugins"

	ENV_FLUENTD_OUTPUT = "FLUENTD_OUTPUT"
	ENV_FLUENTD_WORKER = "FLUENTD_WORKER"
)

var fluentd *exec.Cmd
var _ Piloter = (*FluentdPiloter)(nil)

// FluentdPiloter for fluentd plugin
type FluentdPiloter struct {
	name string
}

// NewFluentdPiloter returns a FluentdPiloter instance
func NewFluentdPiloter() (Piloter, error) {
	return &FluentdPiloter{
		name: PILOT_FLUENTD,
	}, nil
}

// Start starting and watching fluentd process
func (p *FluentdPiloter) Start() error {
	if fluentd != nil {
		pid := fluentd.Process.Pid
		log.Infof("fluentd started, pid: %v", pid)
		return fmt.Errorf(ERR_ALREADY_STARTED)
	}

	log.Info("starting fluentd")
	worker := os.Getenv(ENV_FLUENTD_WORKER)
	if _, err := strconv.Atoi(worker); worker == "" || err != nil {
		worker = "1"
	}

	fluentd = exec.Command(FLUENTD_EXEC_CMD,
		"-c", FLUENTD_CONF_FILE,
		"-p", FLUENTD_PLUGINS,
		"--workers", worker)
	fluentd.Stderr = os.Stderr
	fluentd.Stdout = os.Stdout
	err := fluentd.Start()
	if err != nil {
		log.Errorf("fluentd start fail: %v", err)
	}

	go func() {
		//等待fluentd启动命令退出
		err := fluentd.Wait()
		if err != nil {
			log.Errorf("fluentd exited: %v", err)
			if exitError, ok := err.(*exec.ExitError); ok {
				//处理错误异常退出情况
				processState := exitError.ProcessState
				log.Errorf("fluentd exited pid: %v", processState.Pid())
			}
		}

		// 发现fluentd错误异常退出，尝试重启它
		// try to restart fluentd
		log.Warningf("fluentd exited and try to restart")
		fluentd = nil
		p.Start()
	}()
	return err
}

// Stop log collection
func (p *FluentdPiloter) Stop() error {
	return nil
}

// Reload reload configuration file
func (p *FluentdPiloter) Reload() error {
	if fluentd == nil {
		err := fmt.Errorf("fluentd have not started")
		log.Error(err)
		return err
	}

	log.Info("reload fluentd")
	ch := make(chan struct{})
	//以fluentd启动命令的进程号作为参数传给协程，尝试每隔5秒执行一次reload
	go func(pid int) {
		//pgrep命令以名称为依据从运行进程队列中查找进程，并显示查找到的进程id。"pgrep -P" 指定父进程号，显示该父进程下的所有子进程
		command := fmt.Sprintf("pgrep -P %d", pid)
		childId := shell(command)
		log.Infof("before reload childId : %s", childId)
		//给fluentd启动命令（父进程）发送一个SIGHUP信号：表示终端控制进程结束(终端连接断开)
		fluentd.Process.Signal(syscall.SIGHUP)
		time.Sleep(5 * time.Second)
		afterChildId := shell(command)
		log.Infof("after reload childId : %s", afterChildId)
		//如果reload失败？强制杀死子进程
		//kill -9 pid则是向进程号为pid的进程发送SIGKILL（该信号的编号为9）
		//SIGKILL既不能被应用程序捕获，也不能被阻塞或忽略，其动作是立即结束指定进程。
		//通俗地说，应用程序根本无法“感知”SIGKILL信号，它在完全无准备的情况下，就被收到SIGKILL信号的操作系统给干掉了
		//显然，在这种“暴力”情况下，应用程序完全没有释放当前占用资源的机会。
		//事实上，SIGKILL信号是直接发给init进程的，它收到该信号后，负责终止pid指定的进程。
		//在某些情况下（如进程已经hang死，无法响应正常信号），就可以使用kill -9来结束进程。
		if childId == afterChildId {
			log.Infof("kill childId : %s", childId)
			shell("kill -9 " + childId)
		}
		close(ch)
	}(fluentd.Process.Pid)
	<-ch
	return nil
}

// GetConfPath returns log configuration path
func (p *FluentdPiloter) GetConfPath(container string) string {
	return fmt.Sprintf("%s/%s.conf", FLUENTD_CONF_DIR, container)
}

func shell(command string) string {
	cmd := exec.Command("/bin/sh", "-c", command)
	out, err := cmd.Output()
	if err != nil {
		fmt.Printf("error %v", err)
	}
	return strings.TrimSpace(string(out))
}

// GetConfHome returns configuration directory
func (p *FluentdPiloter) GetConfHome() string {
	return FLUENTD_CONF_DIR
}

// Name returns plugin name
func (p *FluentdPiloter) Name() string {
	return p.name
}

// OnDestroyEvent watching destroy event
func (p *FluentdPiloter) OnDestroyEvent(container string) error {
	log.Info("refactor in the future!!!")
	return nil
}

// GetBaseConf returns plugin root directory
func (p *FluentdPiloter) GetBaseConf() string {
	return FLUENTD_BASE_CONF
}
