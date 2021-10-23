# Localdns-admission-webhook

在ipvs网络模式下，配合 `localdns` 一起使用，通过给 `pod` 资源差异化配置如下的 `dnsPolicy` 和 `dnsConfig`：
```yaml
dnsPolicy: none
dnsConfig: 
  nameservers:
    - 169.254.20.10
  searches:
    - <namespace>.svc.cluster.local
    - svc.cluster.local
    - cluster.local
  options:
    - name: ndots
      value: "2"
```
实现某些业务下的 `pod` 实例才能使用 `localdns` 能力。

通过 `admission webhook` 方案，避免了修改节点上 `kubelet` 参数等相关的运维工作。


## 部署

1. 部署 `localdns daemonset`

```shell script
$ kubectl apply -f ./deploy/localdns-daemonset.yaml
```

2. 部署 `localdns admission webhook`

```shell script
$ kubectl apply -f ./deploy/deploy.yaml
```

## example

前提了解：

- 命令空间限制：命名空间需要加下标签（ `localdns-injector: enabled` )
- `pod` 注解限制：`pod` 对象需要加下注解（ `localdns-policy-webhook/inject: "ture"` ）

``` shell script
$ kubectl label namespace default localdns-injector=enabled

$ cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  annotations:
    localdns-policy-webhook/inject: "ture" 
  name: busybox-sleep
  namespace: default
spec:
  containers:
  - name: busybox
    image: busybox:latest
    args:
    - sleep
    - "1000000"
EOF
```