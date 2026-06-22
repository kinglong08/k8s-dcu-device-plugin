# k8s-dcu-device-plugin
DCU-Device-Plugin负责管理 Kubernetes 集群中的 DCU/vDCU 设备资源分配。它通过 Kubernetes 的设备插件框架（Device Plugin Framework）将 DCU/vDCU 设备暴露给k8s，使应用程序能够直接使用 DCU/vDCU 加速计算任务。支持DCU物理卡、预切分vDCU与动态切分vDCU方案，兼容HAMi项目，从而更灵活地分配计算资源，满足不同应用场景的需求。
