# Hygon DCU Device Plugin

Hygon DCU Device Plugin 是一个 Kubernetes 设备插件（Device Plugin），以 DaemonSet 方式部署到每个 DCU 节点，实现 [Kubernetes Device Plugin API](https://kubernetes.io/docs/concepts/extend-kubernetes/compute-storage-net/device-plugins/)，将节点上的海光 DCU 资源注册到集群，供 Pod 申请使用。

当前版本：**v2.4.2**

## 目录

- [功能特性](#功能特性)
- [使用模式对比](#使用模式对比)
- [架构概览](#架构概览)
- [代码结构](#代码结构)
- [前置要求](#前置要求)
- [资源注册策略](#资源注册策略)
- [配置参数](#配置参数)
- [部署](#部署)
- [使用示例](#使用示例)
- [构建](#构建)
- [验证](#验证)
- [License](#license)

## 功能特性

- **物理 DCU**：发现、注册、健康检查与整卡分配
- **预切分 vDCU**：支持管理员预先划分的虚拟 DCU 实例的注册、健康检查与调度
- **动态切分 vDCU（HAMi 模式）**：Pod 启动时按算力/显存需求动态创建 vDCU，Pod 退出后自动回收
- **预切分 MIG DCU**：支持 MIG 实例的注册、健康检查与调度
- **NUMA 拓扑感知**：向 Kubelet 上报设备的 NUMA 节点信息
- **拓扑信息注册**（HAMi 模式）：将节点 DCU 互联拓扑写入 `kube-system/dcu-topology-info` ConfigMap

## 使用模式对比

| 模式 | 注册策略 | 典型资源 | 是否需要预切分 | 是否需要 Scheduler |
|------|----------|----------|----------------|-------------------|
| 物理整卡 | `dcu` / `mixed` | `hygon.com/dcu` | 否 | 否 |
| 预切分 vDCU | `vdcu` / `mixed` | `hygon.com/dcu-share-4c-16g` | 是（`hy-smi virtual`） | 否 |
| 动态切分 vDCU | `hami` | `hygon.com/dcunum` + `dcucores` + `dcumem` | 否 | 是（[k8s-dcu-scheduler](../k8s-dcu-scheduler)） |
| MIG | `mig` | `hygon.com/dcu-mig-*` | 是（`hy-smi mig`） | 否 |

## 架构概览

### 物理 / 预切分模式

```
┌─────────────────────────────────────────────────────────┐
│                    Kubernetes Node                       │
│  ┌──────────────┐    ┌──────────────────────────────┐   │
│  │   Kubelet    │◄──►│  DCU Device Plugin (DaemonSet)│   │
│  └──────────────┘    │  - ListAndWatch 设备列表      │   │
│                      │  - Allocate 分配设备到容器      │   │
│                      │  - 健康检查 (DCGM)             │   │
│                      └──────────┬───────────────────┘   │
│                                 │ DCGM                   │
│                      ┌──────────▼───────────────────┐   │
│                      │  /dev/dri  /dev/kfd  /dev/mkfd│   │
│                      │  /etc/vdev  /etc/dmi_mig_config│  │
│                      └──────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

### 动态切分 vDCU（HAMi 模式）

```
  Pod (dcunum/dcucores/dcumem)
           │
           ▼
  ┌─────────────────────┐
  │  Admission Webhook   │  改写 schedulerName，写入算力/显存注解
  └─────────┬───────────┘
            ▼
  ┌─────────────────────┐
  │  DCU Scheduler       │  选择节点与物理卡，写入分配注解
  └─────────┬───────────┘
            ▼
  ┌─────────────────────┐
  │  DCU Device Plugin   │  Allocate 阶段动态创建 vDCU，挂载配置文件
  │  (strategy=hami)     │  Pod 退出后自动销毁 vDCU
  └─────────────────────┘
```

## 代码结构

```
k8s-dcu-device-plugin/
├── cmd/
│   └── main.go                          # 程序入口：CLI 参数解析、DCGM 初始化、按策略启动插件
├── internal/pkg/
│   ├── plugin/
│   │   ├── plugin.go                    # Device Plugin 核心：ListAndWatch / Allocate / 健康检查
│   │   └── register.go                  # HAMi 模式：节点注解注册、Pod Informer、拓扑 ConfigMap
│   ├── util/
│   │   ├── dcu.go                       # DCU 设备发现、资源命名、NUMA / 健康检查
│   │   ├── util.go                      # HAMi 调度注解编解码、Pod / Node 补丁
│   │   ├── types.go                     # 设备与容器数据结构、常量定义
│   │   └── client/
│   │       └── client.go                # Kubernetes InCluster / kubeconfig 客户端
│   └── api/
│       └── device_register.go           # 设备信息 API 数据结构
├── deployment/
│   ├── static/                          # kubectl 直部署清单
│   │   ├── k8s-dcu-plugin.yaml          # mixed 模式（物理 DCU + 预切分 vDCU）
│   │   ├── k8s-dcu-plugin-mig.yaml      # MIG 模式
│   │   ├── k8s-dcu-plugin-hami.yaml     # HAMi 动态切分模式（含 RBAC）
│   │   └── k8s-dcu-plugin-hami-dev.yaml # HAMi 开发调试清单
│   └── helm/                            # Helm Chart
│       ├── k8s-dcu-plugin/              # mixed 模式
│       ├── k8s-dcu-plugin-mig/          # MIG 模式
│       └── k8s-dcu-plugin-hami/         # HAMi 模式
├── demo/                                # Pod 使用示例
│   ├── pytorch-dcu.yaml                 # 物理 DCU
│   ├── pytorch-dcu-share.yaml           # 预切分 vDCU
│   ├── pytorch-dcu-mig.yaml             # MIG DCU
│   └── pytorch-dcu-dynamic-vdcu.yaml    # 动态切分 vDCU
├── build.sh                             # 编译二进制、构建镜像、导出离线包
├── Dockerfile                           # 运行时镜像（依赖节点挂载的 /opt/hyhal）
├── go.mod / go.sum                      # Go 模块依赖
├── LICENSE
└── README.md
```

### 核心模块说明

| 模块 | 职责 |
|------|------|
| `cmd/main.go` | 解析 `--strategy` 等 CLI 参数，初始化 DCGM，按策略为每种资源类型启动独立的 Device Plugin 实例 |
| `plugin/plugin.go` | 实现 Kubelet Device Plugin gRPC 接口：`ListAndWatch` 上报设备列表，`Allocate` 将设备挂载到容器 |
| `plugin/register.go` | HAMi 模式下监听 Pod 事件、向节点写入设备注册注解、维护 `dcu-topology-info` ConfigMap |
| `util/dcu.go` | 通过 [dcu-dcgm](https://github.com/HYGON-AI/dcu-dcgm) 发现物理卡 / vDCU / MIG 实例，生成 `hygon.com/*` 资源名 |
| `util/util.go` | 与 [k8s-dcu-scheduler](../k8s-dcu-scheduler) 协作的注解编解码、节点锁、Pod 分配状态管理 |
| `util/client` | 集群内 Kubernetes API 访问（InCluster 优先，回退 kubeconfig） |

### 运行时依赖

| 依赖 | 说明 |
|------|------|
| [dcu-dcgm](https://github.com/HYGON-AI/dcu-dcgm) | Go 模块，提供 DCU 设备发现、vDCU 创建/销毁、健康检查等能力 |
| [Project-HAMi/HAMi](https://github.com/Project-HAMi/HAMi) | HAMi 模式下的节点锁与调度协作工具 |
| [kubevirt/device-plugin-manager](https://github.com/kubevirt/device-plugin-manager) | Device Plugin 生命周期管理框架 |
| 节点 `/opt/hyhal` | 运行时通过 hostPath 挂载，提供 DCU 底层库（镜像内不打包） |

## 前置要求

### 集群与节点

| 项目 | 说明 |
|------|------|
| Kubernetes 集群 | 已安装 DCU 的 Worker 节点 |
| DCU 驱动 | 节点已正确安装 DCU 驱动，`/sys/class/kfd` 存在 |
| hyhal | 节点存在 `/opt/hyhal` 目录，部署时以 hostPath 挂载到插件容器 |
| vDCU 功能 | 驱动版本 ≥ 6.2.26（6.2.26 之后虚拟化命令为 `hy-smi virtual`） |
| 节点标签 | 建议部署 [k8s-dcu-label-node](../k8s-dcu-label-node) 自动为 DCU 节点打上 `hygon.com/dcu=true` 等标签 |
| 动态 vDCU 额外要求 | DTK ≥ 24.04、`hy-smi` ≥ v1.6.0，并部署 [k8s-dcu-scheduler](../k8s-dcu-scheduler) |

### 本地构建

| 项目 | 说明 |
|------|------|
| Go | ≥ 1.22（见 `go.mod`） |
| CGO | 必须启用（`CGO_ENABLED=1`），用于链接 DCGM 库 |
| Docker | 构建容器镜像时需要 |
| DCU 节点 | 编译可在普通机器完成；功能验证需在已安装驱动的 DCU 节点上进行 |

## 资源注册策略

通过 `--strategy` 参数或环境变量 `RESOURCE_REGISTER_STRATEGY` 控制插件注册的资源类型：

| 策略 | 说明 | 注册的资源示例 |
|------|------|----------------|
| `dcu` | 仅注册物理 DCU | `hygon.com/dcu` |
| `vdcu` | 仅注册预切分 vDCU | `hygon.com/dcu-share-4c-16g` |
| `mig` | 仅注册 MIG 实例 | `hygon.com/dcu-mig-4g-31gb` |
| `mixed`（默认） | 同时注册物理 DCU 与 vDCU | `hygon.com/dcu`、`hygon.com/dcu-share-*` |
| `hami` | 动态共享模式 | `hygon.com/dcunum`（可选 `dcucores`、`dcumem`） |

### 资源命名规则（POLICY=0，默认）

- **物理 DCU**：`hygon.com/dcu`
- **预切分 vDCU**：`hygon.com/dcu-share-{CU数}c-{显存GB}g`，例如 `hygon.com/dcu-share-4c-16g`
- **MIG**：`hygon.com/dcu-mig-{规格}`，例如 `hygon.com/dcu-mig-4g-31gb`
- **动态 vDCU**：`hygon.com/dcunum`、`hygon.com/dcucores`、`hygon.com/dcumem`

`POLICY` 参数还支持按设备型号命名（`1`）或按型号+显存+CU 命名（`2`），详见下方配置参数说明。

## 配置参数

| 参数 / 环境变量 | 默认值 | 说明 |
|----------------|--------|------|
| `--strategy` / `RESOURCE_REGISTER_STRATEGY` | `mixed` | 资源注册策略 |
| `--policy` / `POLICY` | `0` | 资源命名策略：`0` 默认命名；`1` 使用设备型号；`2` 型号+显存+CU |
| `--pulse` / `PULSE` | `30` | 设备健康检查间隔（秒） |
| `--node-name` / `NODE_NAME` | - | 当前节点名称（部署时通过 Downward API 注入） |
| `--topology-register` / `TOPOLOGY_REGISTER` | `true` | HAMi 模式下是否注册 DCU 拓扑到 ConfigMap |
| `--resource-multiple` / `RESOURCE_MULTIPLE` | `false` | HAMi 模式下是否额外向 Kubelet 注册 `dcucores` 和 `dcumem` 资源 |
| `--log-verbose` / `LOG_VERBOSE` | `2` | 详细日志级别（0-10） |
| `--stderrthreshold` / `LOG_THRESHOLD` | `INFO` | 日志输出阈值 |
| `--alsologtostderr` / `LOG_OUTPUT` | `true` | 是否输出日志到 stderr |

## 部署

部署清单位于 `deployment/static/`（kubectl 直部署）和 `deployment/helm/`（Helm Chart）。

### 1. 安装 DCU 驱动

在 DCU 节点上安装并加载 DCU 驱动，确认 `hy-smi` 或 `hy-smi virtual` 可正常识别设备。

### 2. 部署节点标签组件（推荐）

```bash
# 部署 k8s-dcu-label-node，自动为 DCU 节点打标签 hygon.com/dcu=true
kubectl apply -f ../k8s-dcu-label-node/deployment/
```

### 3. 物理 DCU + 预切分 vDCU（mixed 模式，默认）

适用于普通 DCU 节点及已预切分 vDCU 的共享节点。DaemonSet 通过节点亲和性调度到 `hygon.com/dcu=true` 且非 MIG/HAMi 专用节点。

```bash
kubectl apply -f deployment/static/k8s-dcu-plugin.yaml
```

**预切分 vDCU 步骤**（在需要共享 DCU 的节点上执行）：

1. 在物理 DCU 上创建 vDCU 实例：

```bash
# 6.2.26 之前
hy-virtual -d ${dev_id} \
  -create-vdevices ${num_vdcu} \
  -vdevice-compute-units $<cu_num, ...> \
  -vdevice-memory-size $<mem_size, ...>

# 6.2.26 及之后
hy-smi virtual -h   # 查看具体用法
```

2. 为节点打标签（可选，用于标识共享模式节点）：

```bash
kubectl label nodes <node-name> dcu-mode=share
```

3. 部署 Device Plugin（mixed 模式会自动发现并注册 vDCU 资源）。

### 4. MIG DCU 模式

1. 在物理 DCU 上创建 MIG 实例：

```bash
hy-smi mig -cgi ${gi_profile_id} -C -i ${dev_id}
# 更多用法：hy-smi mig -h 或参考《Hygon DCU Multi-Instance 使用手册》
```

2. 为节点打标签：

```bash
kubectl label nodes <node-name> dcu-mode=mig
```

3. 部署 MIG Device Plugin：

```bash
kubectl apply -f deployment/static/k8s-dcu-plugin-mig.yaml
```

### 5. 动态切分 vDCU（HAMi 模式）

动态切分模式下，Device Plugin 在 Pod 容器启动的 `Allocate` 阶段调用 DCGM 动态创建 vDCU，Pod 结束后自动销毁，无需管理员提前划分实例。

**部署步骤：**

1. 为节点打标签：

```bash
kubectl label nodes <node-name> dcu=on
```

2. 部署 HAMi Device Plugin（含 RBAC）：

```bash
kubectl apply -f deployment/static/k8s-dcu-plugin-hami.yaml
```

3. 部署 DCU Scheduler 扩展组件（准入 Webhook + 自定义调度器），详见 [k8s-dcu-scheduler 部署文档](../k8s-dcu-scheduler/README.md#部署)：

```bash
# 安装 cert-manager 后
kubectl apply -f ../k8s-dcu-scheduler/deployment/static/vdcu-admission-webhook-certmanager.yaml
kubectl apply -f ../k8s-dcu-scheduler/deployment/static/vdcu-admission-webhook.yaml
kubectl apply -f ../k8s-dcu-scheduler/deployment/static/vdcu-scheduler.yaml
```

4. 确认节点已上报资源：

```bash
kubectl describe node <node-name> | grep -E 'dcunum|dcu-register'
```

### Helm 部署

```bash
# 默认 mixed 模式
helm install dcu-dp deployment/helm/k8s-dcu-plugin/

# MIG 模式
helm install dcu-dp-mig deployment/helm/k8s-dcu-plugin-mig/

# HAMi 模式
helm install dcu-dp-hami deployment/helm/k8s-dcu-plugin-hami/
```

> **注意**：Helm Chart 中的镜像版本可能落后于最新构建，部署前请根据实际情况修改 `values.yaml` 中的 `image.tag`（当前 static 清单使用 `v2.4.2`，Helm 默认 `values.yaml` 可能为较早版本）。

## 使用示例

`demo/` 目录提供了各类 DCU 资源的 Pod 示例，可直接 `kubectl apply -f demo/<文件名>` 使用。

### 物理 DCU（整卡）

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: dcu-pytorch-demo
spec:
  containers:
    - name: dcu-pytorch-demo
      image: harbor.sourcefind.cn:5443/dcu/admin/base/pytorch:2.1.0-ubuntu22.04-dtk24.04.2-py3.10
      command: [ "/bin/bash", "-c", "--" ]
      args: [ "sleep infinity & wait" ]
      resources:
        limits:
          hygon.com/dcu: 1
```

```bash
kubectl apply -f demo/pytorch-dcu.yaml
```

### 预切分 vDCU

管理员需提前在节点上用 `hy-smi virtual` 划分实例，Device Plugin 以 `mixed` 或 `vdcu` 策略注册后，Pod 按规格名称申请：

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: dcu-share-pytorch-demo
spec:
  containers:
    - name: dcu-share-pytorch-demo
      image: harbor.sourcefind.cn:5443/dcu/admin/base/pytorch:2.1.0-ubuntu22.04-dtk24.04.2-py3.10
      securityContext:
        privileged: true
      command: [ "/bin/bash", "-c", "--" ]
      args: [ "sleep infinity & wait" ]
      resources:
        limits:
          hygon.com/dcu-share-4c-16g: 1   # 按节点实际上报的规格名称替换
```

> 每个容器当前仅支持申请 1 个预切分 vDCU 实例。

```bash
kubectl apply -f demo/pytorch-dcu-share.yaml
```

### MIG DCU

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: dcu-mig-pytorch-demo
spec:
  containers:
    - name: dcu-mig-pytorch-demo
      image: harbor.sourcefind.cn:5443/dcu/admin/base/pytorch:2.1.0-ubuntu22.04-dtk24.04.2-py3.10
      command: [ "/bin/bash", "-c", "--" ]
      args: [ "sleep infinity & wait" ]
      resources:
        limits:
          hygon.com/dcu-mig-4g-31gb: 1   # 按实际 MIG Profile 替换
```

```bash
kubectl apply -f demo/pytorch-dcu-mig.yaml
```

### 动态切分 vDCU（HAMi 模式）

Pod 通过三个扩展资源声明 DCU 需求，由 Scheduler 选择物理卡，Device Plugin 在容器启动时动态创建 vDCU：

| 资源 | 含义 | 取值说明 |
|------|------|----------|
| `hygon.com/dcunum` | DCU 槽位数 | 通常为 `1`；当算力或显存非零时，只能为 `1` |
| `hygon.com/dcucores` | 算力占比 | 1–100，`100` 表示独占整卡算力 |
| `hygon.com/dcumem` | 显存申请量 | 单位为 **MB**（默认 `RESOURCE_MULTIPLE=false`） |

**部分共享示例**（申请 30% 算力、8 GB 显存）：

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: dcu-dynamic-vdcu-demo
spec:
  containers:
    - name: dcu-dynamic-vdcu-demo
      image: harbor.sourcefind.cn:5443/dcu/admin/base/pytorch:2.1.0-ubuntu22.04-dtk24.04.2-py3.10
      command: [ "/bin/bash", "-c", "--" ]
      args: [ "sleep infinity & wait" ]
      resources:
        limits:
          hygon.com/dcunum: 1
          hygon.com/dcucores: 30
          hygon.com/dcumem: 8192
```

**整卡示例**（申请 100% 算力与全部显存，不创建 vDCU，直接使用物理卡）：

```yaml
resources:
  limits:
    hygon.com/dcunum: 1
    hygon.com/dcucores: 100
    hygon.com/dcumem: 32768   # 设为物理卡总显存（MB）
```

```bash
kubectl apply -f demo/pytorch-dcu-dynamic-vdcu.yaml
```

> 准入 Webhook 启用后会自动将 Pod 的 `schedulerName` 改写为 DCU 调度器；如未部署 Webhook，需手动设置 `spec.schedulerName: dcu-scheduler-plugin`。

**验证动态 vDCU 分配结果：**

```bash
# 查看 Pod 调度与绑定状态
kubectl get pod dcu-dynamic-vdcu-demo -o wide
kubectl describe pod dcu-dynamic-vdcu-demo | grep -E 'Annotations|hygon.com'

# 进入容器查看实际分配到的 vDCU
kubectl exec -it dcu-dynamic-vdcu-demo -- bash -c "source /opt/hygondriver/env.sh && hy-virtual -show-device-info"
```

预期输出类似：

```
Device 0:
        Actual Device: 0
        Compute units: 9
        Global memory: 8589934592 bytes
```

**Pod 退出后**，Device Plugin 会自动停止并销毁对应的动态 vDCU 实例，释放物理卡资源。

更多 Deployment / Job 等多容器示例见 [k8s-dcu-scheduler/example](../k8s-dcu-scheduler/example/) 目录。

## 构建

```bash
# 编译二进制、构建 Docker 镜像、导出离线 tar 包
./build.sh
```

构建产物：

| 产物 | 路径 / 名称 |
|------|-------------|
| 二进制 | `k8s-device-plugin` |
| 镜像 | `harbor.sourcefind.cn:5443/dcu/admin/base/dcu-device-plugin:v2.4.2` |
| 离线包 | `dcu-device-plugin-v2.4.2.tar` |

手动编译：

```bash
export CGO_ENABLED=1
go mod tidy
go build -ldflags "-X 'main.version=v2.4.2'" -o k8s-device-plugin cmd/main.go
```

> 镜像运行时通过 `LD_LIBRARY_PATH` 加载节点挂载的 `/opt/hyhal/lib`，无需在镜像内打包 DCU 底层库。

## 验证

```bash
# 查看 Device Plugin Pod 状态（mixed 模式）
kubectl get pods -n kube-system -l name=dcu-dp-ds

# HAMi 模式
kubectl get pods -n kube-system -l name=dcu-dp-ds-hami

# 查看节点 DCU 资源
kubectl describe node <node-name> | grep hygon.com

# 查看 Device Plugin 日志
kubectl logs -n kube-system -l name=dcu-dp-ds
```

## License

本项目部分代码基于 [HAMi](https://github.com/Project-HAMi/HAMi) 改编，Hygon 的修改与原创贡献均采用 [Apache License 2.0](LICENSE)。
