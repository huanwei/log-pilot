
默认tomcat pod内1号进程为java
```$xslt
[root@10 ~]# kubectl exec -it tomcat sh
/usr/local/tomcat # ps -ef
PID   USER     TIME   COMMAND
    1 root       1:05 /usr/lib/jvm/java-1.8-openjdk/jre/bin/java -Djava.util.logging.config.file=/usr/local/tomcat/conf/logging.properties -Djava.util.logging.ma
   84 root       0:00 sh
   89 root       0:00 ps -ef
```


tomcat pod开启PID共享，修改pod yaml:
``
spec:
  shareProcessNamespace: true
``
1号进程变为pause

```
[root@10 examples]# kubectl exec -it tomcat sh
/usr/local/tomcat # ps -ef
PID   USER     TIME   COMMAND
    1 root       0:00 /pause
    6 root       0:04 /usr/lib/jvm/java-1.8-openjdk/jre/bin/java -Djava.util.logging.config.file=/usr/local/tomcat/conf/logging.properties -Djava.util.logging.ma
   60 root       0:00 sh
   65 root       0:00 ps -ef
```

https://kubernetes.io/docs/tasks/configure-pod-container/share-process-namespace/
