/*
 * SPDX-License-Identifier: Apache-2.0
 * Copyright 2026 Hygon Information Technology Co., Ltd.
 */

package util

import (
	"fmt"
	"g.sugon.com/das/dcgm-dcu/pkg/dcgm"
	"github.com/golang/glog"
	corev1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const RESOURCE_REGISTER_STRATEGY = "RESOURCE_REGISTER_STRATEGY"

// GetCardAndRender get DCU render and card index from driver module
func GetCardAndRender(pcieAddress string) ([]string, error) {
	modules := []string{"amdgpu", "hydcu", "hycu"}

	for _, module := range modules {
		// 构建目录路径
		dirPath := filepath.Join("/sys/module", module, "drivers/pci:"+module, pcieAddress, "drm")
		if _, err := os.Stat(dirPath); os.IsNotExist(err) {
			continue
		}

		// 打开目录
		dir, err := os.Open(dirPath)
		if err != nil {
			glog.Errorf("error open directory %s: %v", dirPath, err)
			return nil, fmt.Errorf("error open directory %s: %v", dirPath, err)
		}
		defer dir.Close()

		subDirs, err := dir.Readdirnames(-1)
		if err != nil {
			glog.Errorf("read directory %s error: %v", dirPath, err)
			return nil, fmt.Errorf("read directory %s error: %v", dirPath, err)
		}

		return subDirs, nil
	}

	glog.Error("no matching modules found (amdgpu, hydcu, hycu)")
	return nil, fmt.Errorf("no matching modules found (amdgpu, hydcu, hycu)")
}

// SimpleHealthCheck check DCU healthy by index
func SimpleHealthCheck(index int) bool {
	temp, err := dcgm.Temperature(index)
	if err != nil {
		glog.Error("Error health check through DCGM temperature function")
		return false
	}

	if temp > 0 {
		return true
	}
	return false
}

func GetNumaNode(index int) (*pluginapi.TopologyInfo, error) {
	// Get numa information for DCU
	numas, err := dcgm.ShowNumaTopology([]int{index})
	glog.V(3).Infof("Watching DCU with DCU Index: %d NUMA Node: %+v", index, numas)
	if err != nil || len(numas) == 0 {
		glog.Errorf("Get NUMA info error: %v", err)
		return &pluginapi.TopologyInfo{}, err
	}

	numaNodes := make([]*pluginapi.NUMANode, len(numas))
	for j, v := range numas {
		numaNodes[j] = &pluginapi.NUMANode{
			ID: int64(v.NumaNode),
		}
	}

	return &pluginapi.TopologyInfo{
		Nodes: numaNodes,
	}, nil
}

// GetAllVirtualDCUs get vDCUs in map format
func GetAllVirtualDCUs() map[string][]dcgm.VDeviceInfo {
	virtualDCUInfos, err := dcgm.VDeviceInfos()
	if err != nil {
		glog.Errorf("Get device infos error: %v ", err)
	}
	glog.V(2).Infof("Get Virtual DCU number : %d \n", len(virtualDCUInfos))

	virtualDCUs := make(map[string][]dcgm.VDeviceInfo)
	for _, virtualDCU := range virtualDCUInfos {
		cu_string := strconv.Itoa(virtualDCU.VComputeUnitCount) + "c"
		mem_string := strconv.Itoa(int(math.Round(float64(virtualDCU.VMemoryTotal)/(1024*1024*1024)))) + "g"
		vkey := "share-" + cu_string + "-" + mem_string
		_, exists := virtualDCUs[vkey]
		if exists {
			virtualDCUs[vkey] = append(virtualDCUs[vkey], virtualDCU)
		} else {
			virtualDCUs[vkey] = []dcgm.VDeviceInfo{virtualDCU}
		}
	}
	return virtualDCUs
}

// GetAllMigDCUs get all  MIG DCUs in map format
func GetAllMigDCUs() map[string][]dcgm.MigInfo {
	migDeviceInfos, err := dcgm.MigInfos()
	if err != nil {
		glog.Errorf("Get device infos error: %v ", err)
	}
	glog.V(2).Infof("Get MIG DCUs number: %d \n", len(migDeviceInfos))

	migDCUs := make(map[string][]dcgm.MigInfo)
	for _, migDeviceInfo := range migDeviceInfos {
		migDeviceName := migDeviceInfo.Name
		migDeviceProfile := strings.TrimPrefix(migDeviceName, "MIG ")
		migDeviceProfile = strings.ReplaceAll(migDeviceProfile, ".", "-")
		migKey := "mig-" + migDeviceProfile
		_, exists := migDCUs[migKey]
		if exists {
			migDCUs[migKey] = append(migDCUs[migKey], migDeviceInfo)
		} else {
			migDCUs[migKey] = []dcgm.MigInfo{migDeviceInfo}
		}
	}
	return migDCUs
}

// GetAllPhysicalDevices Get All Physical DCUs
func GetAllPhysicalDevices() map[string]dcgm.DeviceInfo {
	physicalDeviceInfos := make(map[string]dcgm.DeviceInfo)
	dcuDeviceInfos, err := dcgm.DeviceInfos()
	if err != nil {
		glog.Error("Error get physical DCU device information")
	}
	for _, dcuDeviceInfo := range dcuDeviceInfos {
		physicalDeviceInfos[dcuDeviceInfo.PciBusNumber] = dcuDeviceInfo
	}
	return physicalDeviceInfos
}

func RequestsDCU(pod *corev1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		for resourceName, quantity := range container.Resources.Limits {
			if (strings.Contains(string(resourceName), "hygon.com")) && quantity.Value() > 0 {
				return true
			}
		}
	}
	return false
}

func RequestsVirtualDCU(pod *corev1.Pod) bool {
	for _, container := range pod.Spec.Containers {
		for resourceName, quantity := range container.Resources.Limits {
			if strings.Contains(string(resourceName), "hygon.com") && strings.Contains(string(resourceName), "dcumem") && quantity.Value() > 0 {
				return true
			}
		}
	}
	return false
}

func GetResourceNamePrefix(policy string) string {
	info, err := dcgm.GetDeviceInfo(0)
	if err != nil {
		glog.Error("Get device info err: %v", err)
	}

	if policy == "1" {
		return info.Name
	}

	if policy == "2" {
		cu_string := strconv.Itoa(info.ComputeUnitCount) + "CU"
		mem_string := strconv.Itoa(int(math.Round(float64(info.GlobalMemSize)/(1024*1024*1024)))) + "G"
		return strings.ReplaceAll(info.Name, "_", "-") + "_" + mem_string + "_" + cu_string
	}
	return "dcu"
}

func GetHAMiResourceName(policy string) string {
	info, err := dcgm.GetDeviceInfo(0)
	if err != nil {
		glog.Error("Get device info err: %v", err)
	}

	if policy == "1" {
		return info.Name + "_dcunum"
	}

	if policy == "2" {
		cu_string := strconv.Itoa(info.ComputeUnitCount) + "CU"
		mem_string := strconv.Itoa(int(math.Round(float64(info.GlobalMemSize)/(1024*1024*1024)))) + "G"
		return strings.ReplaceAll(info.Name, "_", "-") + "_" + mem_string + "_" + cu_string + "_dcunum"
	}
	return "dcunum"
}
