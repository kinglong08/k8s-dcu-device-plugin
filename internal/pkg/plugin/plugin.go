/*
 * SPDX-License-Identifier: Apache-2.0
 * Copyright 2026 Hygon Information Technology Co., Ltd.
 */

package plugin

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/HYGON-AI/dcu-exporter-v2/internal/pkg/util"

	"github.com/HYGON-AI/dcu-dcgm/pkg/dcgm"
	hmutil "github.com/Project-HAMi/HAMi/pkg/util"
	"github.com/golang/glog"
	"github.com/kubevirt/device-plugin-manager/pkg/dpm"
	"golang.org/x/net/context"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

// NodeLockDCU should same with hami scheduler hygon device NodeLockDCU
// there is a bug with nodelock package utils, the key is hard coded as "hami.io/mutex.lock"
// so we can only use this value now.
const (
	NodeLockDCU = "hami.io/mutex.lock"
)

type DevicePlugin struct {
	DCUs        map[string]dcgm.DeviceInfo
	VirtualDCUs map[string]dcgm.VDeviceInfo
	MigDCUs     map[string]dcgm.MigInfo
	HAMiDCUs    map[string]dcgm.DeviceInfo
	Heartbeat   chan bool
	signal      chan os.Signal
	Resource    string
}

type DevicePluginOption func(*DevicePlugin)

func NewDevicePlugin(options ...DevicePluginOption) *DevicePlugin {
	DevicePlugin := &DevicePlugin{}
	for _, option := range options {
		option(DevicePlugin)
	}
	return DevicePlugin
}

func WithHeartbeat(ch chan bool) DevicePluginOption {
	return func(p *DevicePlugin) {
		p.Heartbeat = ch
	}
}
func WithResource(res string) DevicePluginOption {
	return func(p *DevicePlugin) {
		p.Resource = res
	}
}

// Start is an optional interface that could be implemented by plugin.
// If case Start is implemented, it will be executed by Manager after
// plugin instantiation and before its registration to kubelet. This
// method could be used to prepare resources before they are offered
// to Kubernetes.
func (p *DevicePlugin) Start() error {
	p.signal = make(chan os.Signal, 1)
	signal.Notify(p.signal, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)

	// refresh container devices and node annotation
	strategy := os.Getenv(util.RESOURCE_REGISTER_STRATEGY)
	if strategy == "hami" {
		go p.WatchAndRegister()
		go p.informerPodHandler()
	}
	return nil
}

// Stop is an optional interface that could be implemented by plugin.
// If case Stop is implemented, it will be executed by Manager after the
// plugin is unregistered from kubelet. This method could be used to tear
// down resources.
func (p *DevicePlugin) Stop() error {
	return nil
}

// GetDevicePluginOptions returns options to be communicated with Device
// Manager
func (p *DevicePlugin) GetDevicePluginOptions(ctx context.Context, e *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{
		GetPreferredAllocationAvailable: true,
	}, nil
}

// PreStartContainer is expected to be called before each container start if indicated by plugin during registration phase.
// PreStartContainer allows kubelet to pass reinitialized devices to containers.
// PreStartContainer allows Device Plugin to run device specific operations on the Devices requested
func (p *DevicePlugin) PreStartContainer(ctx context.Context, r *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}

// ListAndWatch returns a stream of List of Devices
// Whenever a Device state change or a Device disappears, ListAndWatch
// returns the new list
func (p *DevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {

	resourceNamePrefix := util.GetResourceNamePrefix(os.Getenv("POLICY"))
	if p.Resource == resourceNamePrefix {
		return p.ListAndWatchDCUs(e, s)
	}

	if strings.Contains(p.Resource, "share") {
		return p.ListAndWatchVirtualDCUs(e, s)
	}

	if strings.Contains(p.Resource, "mig") {
		return p.ListAndWatchMigDCUs(e, s)
	}

	if strings.Contains(p.Resource, "dcunum") {
		return p.ListAndWatchHAMiDCUs(e, s)
	}
	if strings.Contains(p.Resource, "dcucores") {
		return p.ListAndWatchDCUCores(e, s)
	}
	if strings.Contains(p.Resource, "dcumem") {
		return p.ListAndWatchDCUMem(e, s)
	}
	return nil
}

// ListAndWatchDCUs
func (p *DevicePlugin) ListAndWatchDCUs(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	strategy := os.Getenv(util.RESOURCE_REGISTER_STRATEGY)

	p.DCUs = util.GetAllPhysicalDevices()
	for deviceID, physicalDevice := range p.DCUs {
		if strategy == "mixed" && physicalDevice.VDeviceCount > 0 {
			delete(p.DCUs, deviceID)
		}
	}

	glog.V(2).Infof("Found %d DCUs", len(p.DCUs))

	devs := buildDevicesWithNUMAFromDCUInfo(p.DCUs)

	return p.watchDevices(s, devs, func(devs []*pluginapi.Device) {
		var health = pluginapi.Unhealthy

		// update with per device GPU health status
		glog.V(2).Infof("Starting DCU Health Check!")
		for i := 0; i < len(p.DCUs); i++ {
			if util.SimpleHealthCheck(p.DCUs[devs[i].ID].DvInd) {
				health = pluginapi.Healthy
			} else {
				glog.Errorf("Health Check Faild!")
			}
			devs[i].Health = health
		}
		glog.V(2).Infof("Finishing DCU Health Check!")
	})
}

func (p *DevicePlugin) ListAndWatchDCUCores(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	deviceCount, _ := dcgm.DeviceCount()
	devs := make([]*pluginapi.Device, deviceCount*100)
	func() {
		i := 0
		for id := 0; id < deviceCount*100; id++ {
			dev := &pluginapi.Device{
				ID:     strconv.Itoa(id),
				Health: pluginapi.Healthy,
			}

			devs[i] = dev
			i++
		}
	}()

	return p.watchDevices(s, devs, nil)
}

func (p *DevicePlugin) ListAndWatchDCUMem(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	deviceInfos, _ := dcgm.DeviceInfos()
	memSize := 0
	for _, deviceInfo := range deviceInfos {
		memSize += int(math.Round(float64(deviceInfo.MemoryTotal) / (1024 * 1024 * 1024)))
	}

	devs := make([]*pluginapi.Device, memSize)
	func() {
		i := 0
		for id := 0; id < memSize; id++ {
			dev := &pluginapi.Device{
				ID:     strconv.Itoa(id),
				Health: pluginapi.Healthy,
			}

			devs[i] = dev
			i++

		}
	}()

	return p.watchDevices(s, devs, nil)
}

func (p *DevicePlugin) ListAndWatchVirtualDCUs(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {

	allVirtualDCUs := util.GetAllVirtualDCUs()
	resourceName := "share" + strings.Split(p.Resource, "share")[1]

	_, exists := allVirtualDCUs[resourceName]
	if exists {
		p.VirtualDCUs = make(map[string]dcgm.VDeviceInfo, len(allVirtualDCUs[resourceName]))
		for _, virtualDCU := range allVirtualDCUs[resourceName] {
			p.VirtualDCUs["vdev"+strconv.Itoa(virtualDCU.VdvInd)] = virtualDCU
		}
	}

	glog.V(2).Infof("Found %d Virtual DCUs", len(p.VirtualDCUs))

	devs := buildDevicesWithNUMAFromVDeviceInfo(p.VirtualDCUs)

	return p.watchDevices(s, devs, func(devs []*pluginapi.Device) {
		var health = pluginapi.Unhealthy

		// update with per device GPU health status
		glog.V(2).Infof("Starting Virtual DCUs Health Check!")
		for i := 0; i < len(p.VirtualDCUs); i++ {
			if util.SimpleHealthCheck(p.VirtualDCUs[devs[i].ID].DvInd) {
				health = pluginapi.Healthy
			} else {
				glog.Errorf("Health Check Faild!")
			}
			devs[i].Health = health
		}
		glog.V(2).Infof("Finishing Virtual DCUs Health Check!")
	})
}

func (p *DevicePlugin) ListAndWatchMigDCUs(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {

	allMigDCUs := util.GetAllMigDCUs()
	resourceName := "mig" + strings.Split(p.Resource, "mig")[1]
	_, exists := allMigDCUs[resourceName]
	if exists {
		p.MigDCUs = make(map[string]dcgm.MigInfo, len(allMigDCUs[resourceName]))
		for _, migDCU := range allMigDCUs[resourceName] {
			p.MigDCUs[migDCU.UUID] = migDCU
		}
	}

	glog.V(2).Infof("Found %d MIG DCUs", len(p.MigDCUs))

	devs := buildDevicesWithNUMAFromMigInfo(p.MigDCUs)

	return p.watchDevices(s, devs, func(devs []*pluginapi.Device) {
		var health = pluginapi.Unhealthy

		// update with per device GPU health status
		glog.V(2).Infof("Starting MIG DCUs Health Check!")
		for i := 0; i < len(p.MigDCUs); i++ {
			if util.SimpleHealthCheck(p.MigDCUs[devs[i].ID].DvInd) {
				health = pluginapi.Healthy
			} else {
				glog.Errorf("Health Check Faild!")
			}
			devs[i].Health = health
		}
		glog.V(2).Infof("Finishing  MIG Health Check!")
	})
}

func (p *DevicePlugin) ListAndWatchHAMiDCUs(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {

	allPhysicalDevices := util.GetAllPhysicalDevices()
	allHAMiDCUs := make(map[string]dcgm.DeviceInfo, len(allPhysicalDevices)*4)
	for _, physicalDevice := range allPhysicalDevices {
		for idx := 0; idx < 4; idx++ {
			allHAMiDCUs["DCU-"+physicalDevice.DeviceId+"-fake-"+strconv.Itoa(idx)] = physicalDevice
		}
	}
	p.HAMiDCUs = allHAMiDCUs

	glog.V(2).Infof("Found %d HAMi DCUs", len(p.HAMiDCUs))

	devs := buildDevicesWithNUMAFromDCUInfo(p.HAMiDCUs)

	return p.watchDevices(s, devs, func(devs []*pluginapi.Device) {
		var health = pluginapi.Unhealthy

		// update with per device GPU health status
		glog.V(2).Infof("Starting HAMi DCUs Health Check!")
		for i := 0; i < len(p.HAMiDCUs); i++ {
			if util.SimpleHealthCheck(p.HAMiDCUs[devs[i].ID].DvInd) {
				health = pluginapi.Healthy
			} else {
				glog.Errorf("Health Check Faild!")
			}
			devs[i].Health = health
		}
		glog.V(2).Infof("Finishing HAMi DCUs Health Check!")
	})
}

// watchDevices sends initial device list and then handles heartbeat and signal events.
// If updateHealth is not nil, it will be called on every heartbeat to refresh device health.
func (p *DevicePlugin) watchDevices(
	s pluginapi.DevicePlugin_ListAndWatchServer,
	devs []*pluginapi.Device,
	updateHealth func([]*pluginapi.Device),
) error {
	s.Send(&pluginapi.ListAndWatchResponse{Devices: devs})

loop:
	for {
		select {
		case <-p.Heartbeat:
			if updateHealth != nil {
				updateHealth(devs)
			}
			s.Send(&pluginapi.ListAndWatchResponse{Devices: devs})
		case <-p.signal:
			glog.V(2).Infof("Received signal, exiting")
			break loop
		}
	}

	// returning a value with this function will unregister the plugin from k8s
	return nil
}

// GetPreferredAllocation returns a preferred set of devices to allocate
// from a list of available ones. The resulting preferred allocation is not
// guaranteed to be the allocation ultimately performed by the
// devicemanager. It is only designed to help the devicemanager make a more
// informed allocation decision when possible.
func (p *DevicePlugin) GetPreferredAllocation(ctx context.Context, req *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return &pluginapi.PreferredAllocationResponse{}, nil
}

// Allocate is called during container creation so that the Device
// Plugin can run device specific operations and instruct Kubelet
// of the steps to make the Device available in the container
func (p *DevicePlugin) Allocate(ctx context.Context, r *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resourceNamePrefix := util.GetResourceNamePrefix(os.Getenv("POLICY"))
	if p.Resource == resourceNamePrefix {
		return p.AllocateDCUs(ctx, r)
	}

	if strings.Contains(p.Resource, "share") {
		return p.AllocateVirtualDCUs(ctx, r)
	}

	if strings.Contains(p.Resource, "mig") {
		return p.AllocateMigDCUs(ctx, r)
	}

	if strings.Contains(p.Resource, "dcunum") {
		return p.AllocateHAMiDCUs(ctx, r)
	}

	if strings.Contains(p.Resource, "dcucores") || strings.Contains(p.Resource, "dcumem") {
		return p.AllocateCores(ctx, r)
	}

	return &pluginapi.AllocateResponse{}, nil
}

func (p *DevicePlugin) AllocateDCUs(ctx context.Context, r *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	var response pluginapi.AllocateResponse
	var car pluginapi.ContainerAllocateResponse

	for _, req := range r.ContainerRequests {
		car = pluginapi.ContainerAllocateResponse{}

		addCommonDevicesAndMounts(&car)

		for _, id := range req.DevicesIDs {
			glog.V(2).Infof("Allocating device Bus ID: %s", id)
			//Get render and card index path
			cardAndRenderNames, err := util.GetCardAndRender(id)
			if err != nil {
				glog.Errorf("Device Card and Render Found Error by BUS id %s, Error:%v", id, err)
				return &pluginapi.AllocateResponse{}, fmt.Errorf("device Card and Render Found Error by BUS id %s, Error:%v", id, err)
			}
			for _, devPath := range cardAndRenderNames {
				devCardPath := "/dev/dri/" + devPath
				devCard := new(pluginapi.DeviceSpec)
				devCard.HostPath = devCardPath
				devCard.ContainerPath = devCardPath
				devCard.Permissions = "rw"
				car.Devices = append(car.Devices, devCard)
			}
		}

		response.ContainerResponses = append(response.ContainerResponses, &car)
	}

	return &response, nil
}

func (p *DevicePlugin) AllocateVirtualDCUs(ctx context.Context, r *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	var response pluginapi.AllocateResponse
	var car pluginapi.ContainerAllocateResponse

	for _, req := range r.ContainerRequests {
		car = pluginapi.ContainerAllocateResponse{}

		addCommonDevicesAndMounts(&car)

		// Mount requested devices
		if len(req.DevicesIDs) > 1 {
			glog.Warningf("In the beta version, each container is allowed to use only one share device. ")
			glog.Warningf("The first configuration files will take effect, while the others will not take effect.")
		}

		for _, id := range req.DevicesIDs {
			glog.V(2).Infof("Allocating device ID: %s", id)
			//Get render and card index path
			cardAndRenderNames, err := util.GetCardAndRender(p.VirtualDCUs[id].PciBusNumber)
			if err != nil {
				glog.Errorf("Device Card and Render Found Error by BUS id %s, Error:%v", p.VirtualDCUs[id].PciBusNumber, err)
				return &pluginapi.AllocateResponse{}, fmt.Errorf("device Card and Render Found Error by BUS id %s, Error:%v", p.VirtualDCUs[id].PciBusNumber, err)
			}
			for _, devPath := range cardAndRenderNames {
				devCardPath := "/dev/dri/" + devPath
				devCard := new(pluginapi.DeviceSpec)
				devCard.HostPath = devCardPath
				devCard.ContainerPath = devCardPath
				devCard.Permissions = "rw"
				car.Devices = append(car.Devices, devCard)
			}

			mount := new(pluginapi.Mount)
			hostpath := fmt.Sprintf("/etc/vdev/%s.conf", id)
			containerpath := fmt.Sprintf("/etc/vdev/docker/%s.conf", id)
			mount.HostPath = hostpath
			mount.ContainerPath = containerpath
			mount.ReadOnly = true
			car.Mounts = append(car.Mounts, mount)
		}

		response.ContainerResponses = append(response.ContainerResponses, &car)
	}

	return &response, nil
}

func (p *DevicePlugin) AllocateMigDCUs(ctx context.Context, r *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	var response pluginapi.AllocateResponse
	var car pluginapi.ContainerAllocateResponse

	for _, req := range r.ContainerRequests {
		car = pluginapi.ContainerAllocateResponse{}

		addCommonDevicesAndMounts(&car)

		for _, id := range req.DevicesIDs {
			glog.V(2).Infof("Allocating MIG device %s with physical device %s", id, p.MigDCUs[id].PciBusNumber)

			//Get render and card index path
			cardAndRenderNames, err := util.GetCardAndRender(p.MigDCUs[id].PciBusNumber)
			if err != nil {
				glog.Errorf("Device Card and Render Found Error by BUS id %s, Error:%v", p.MigDCUs[id].PciBusNumber, err)
				return &pluginapi.AllocateResponse{}, fmt.Errorf("device Card and Render Found Error by BUS id %s, Error:%v", p.MigDCUs[id].PciBusNumber, err)
			}
			for _, devPath := range cardAndRenderNames {
				devCardPath := "/dev/dri/" + devPath
				devCard := new(pluginapi.DeviceSpec)
				devCard.HostPath = devCardPath
				devCard.ContainerPath = devCardPath
				devCard.Permissions = "rw"
				car.Devices = append(car.Devices, devCard)
			}

			mount := new(pluginapi.Mount)
			//Use dcgm to obtain the GI, CI instance ID, and physical card ID of the MIG instance
			migInstance, err := dcgm.MigInfoByUUID(id)
			if err != nil {
				glog.Errorf("Get MIG instance error: %v", err)
			}
			dcuID := migInstance.DvInd
			giID := migInstance.GpuInstanceId
			ciID := migInstance.ComputeInstanceId
			confFileName := "dev" + strconv.Itoa(dcuID) + "gi" + strconv.FormatUint(uint64(giID), 10) + "ci" + strconv.FormatUint(uint64(ciID), 10)
			hostpath := fmt.Sprintf("/etc/dmi_mig_config/ci/%s.conf", confFileName)
			containerpath := fmt.Sprintf("/etc/dmi_mig_config/ci/%s.conf", confFileName)
			mount.HostPath = hostpath
			mount.ContainerPath = containerpath
			mount.ReadOnly = true
			car.Mounts = append(car.Mounts, mount)
		}

		response.ContainerResponses = append(response.ContainerResponses, &car)
	}

	return &response, nil
}

// addDeviceIfExists appends a device spec to the container response if the given path exists.
func addDeviceIfExists(path string, car *pluginapi.ContainerAllocateResponse) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		glog.Warningf("can not find %s.", path)
		return
	}
	if err != nil {
		glog.Errorf("Error occurred when checking %s.", path)
		return
	}

	if path == "/dev/mkfd" {
		glog.Infof("preparing mkfd ")
	}

	dev := &pluginapi.DeviceSpec{
		HostPath:      path,
		ContainerPath: path,
		Permissions:   "rw",
	}
	car.Devices = append(car.Devices, dev)
}

// addCommonDevicesAndMounts adds common device nodes and mounts shared by several Allocate methods.
func addCommonDevicesAndMounts(car *pluginapi.ContainerAllocateResponse) {
	// Currently, there are only 1 /dev/kfd and mkfd per node regardless of the # of GPU available
	// for compute use cases.
	addDeviceIfExists("/dev/kfd", car)
	addDeviceIfExists("/dev/mkfd", car)

	car.Mounts = append(car.Mounts, &pluginapi.Mount{
		ContainerPath: "/opt/hyhal",
		HostPath:      "/opt/hyhal",
		ReadOnly:      true,
	})
}

// addCommonRWMDevices adds /dev/kfd and /dev/mkfd with rwm permissions (used by HAMi and cores allocation).
func addCommonRWMDevices(car *pluginapi.ContainerAllocateResponse) {
	paths := []string{"/dev/kfd", "/dev/mkfd"}
	for _, path := range paths {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			glog.Warningf("can not find %s.", path)
			continue
		} else if err != nil {
			glog.Errorf("Error occurred when checking %s.", path)
			continue
		}

		if path == "/dev/mkfd" {
			glog.Infof("preparing mkfd ")
		}

		dev := &pluginapi.DeviceSpec{
			HostPath:      path,
			ContainerPath: path,
			Permissions:   "rwm",
		}
		car.Devices = append(car.Devices, dev)
	}
}

// buildDevicesWithNUMAFromDCUInfo builds pluginapi.Device slice from a map keyed by ID with dcgm.DeviceInfo values.
func buildDevicesWithNUMAFromDCUInfo(devices map[string]dcgm.DeviceInfo) []*pluginapi.Device {
	devs := make([]*pluginapi.Device, len(devices))
	i := 0
	for id, device := range devices {
		dev := &pluginapi.Device{
			ID:     id,
			Health: pluginapi.Healthy,
		}

		numaInfo, err := util.GetNumaNode(device.DvInd)
		if err == nil {
			dev.Topology = numaInfo
		}

		devs[i] = dev
		i++
	}
	return devs
}

// buildDevicesWithNUMAFromVDeviceInfo builds pluginapi.Device slice from a map keyed by ID with dcgm.VDeviceInfo values.
func buildDevicesWithNUMAFromVDeviceInfo(devices map[string]dcgm.VDeviceInfo) []*pluginapi.Device {
	devs := make([]*pluginapi.Device, len(devices))
	i := 0
	for id, device := range devices {
		dev := &pluginapi.Device{
			ID:     id,
			Health: pluginapi.Healthy,
		}

		numaInfo, err := util.GetNumaNode(device.DvInd)
		if err == nil {
			dev.Topology = numaInfo
		}

		devs[i] = dev
		i++
	}
	return devs
}

// buildDevicesWithNUMAFromMigInfo builds pluginapi.Device slice from a map keyed by ID with dcgm.MigInfo values.
func buildDevicesWithNUMAFromMigInfo(devices map[string]dcgm.MigInfo) []*pluginapi.Device {
	devs := make([]*pluginapi.Device, len(devices))
	i := 0
	for id, device := range devices {
		dev := &pluginapi.Device{
			ID:     id,
			Health: pluginapi.Healthy,
		}

		numaInfo, err := util.GetNumaNode(device.DvInd)
		if err == nil {
			dev.Topology = numaInfo
		}

		devs[i] = dev
		i++
	}
	return devs
}

func (p *DevicePlugin) AllocateHAMiDCUs(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	var car pluginapi.ContainerAllocateResponse
	responses := pluginapi.AllocateResponse{}
	nodename := util.NodeName
	current, err := hmutil.GetPendingPod(ctx, nodename)
	if err != nil {
		//nodelock.ReleaseNodeLock(nodename, NodeLockDCU, current, false)
		car = pluginapi.ContainerAllocateResponse{}
		addCommonRWMDevices(&car)

		responses.ContainerResponses = append(responses.ContainerResponses, &car)
		return &responses, nil
	}
	glog.V(2).Infof("Allocate for pod %s/%s uid [%s] \n", current.Namespace, current.Name, current.UID)

	err = util.UpdateContainerIndexAnnotations(current)
	if err != nil {
		return &pluginapi.AllocateResponse{}, err
	}

	current, err = util.GetPod(ctx, current.Namespace, current.Name)
	if err != nil {
		return &pluginapi.AllocateResponse{}, err
	}
	ctrIndex := util.GetCurrentContainerIndex(current)

	for idx := range reqs.ContainerRequests {
		_, devreq, err := util.GetNextDeviceRequest(util.HygonDCUDevice, *current)
		glog.V(2).Infoln("deviceAllocateFromAnnotation=", devreq)
		if err != nil {
			util.PodAllocationFailed(nodename, current, NodeLockDCU)
			return &pluginapi.AllocateResponse{}, err
		}
		if len(devreq) != len(reqs.ContainerRequests[idx].DevicesIDs) {
			util.PodAllocationFailed(nodename, current, NodeLockDCU)
			return &pluginapi.AllocateResponse{}, errors.New("device number not matched")
		}

		err = util.EraseNextDeviceTypeFromAnnotation(util.HygonDCUDevice, *current)
		if err != nil {
			util.PodAllocationFailed(nodename, current, NodeLockDCU)
			return &pluginapi.AllocateResponse{}, err
		}

		car = pluginapi.ContainerAllocateResponse{}
		addCommonRWMDevices(&car)

		var devSerialNumber = ""
		for _, val := range devreq {
			glog.Infof("Allocating device Serial Number: %s", val.UUID)
			succeedCount, err := fmt.Sscanf(val.UUID, "DCU-%s", &devSerialNumber)
			if err != nil || succeedCount == 0 || devSerialNumber == "" {
				glog.Errorf("Invalid request device uuid: %s", val.UUID)
				util.PodAllocationFailed(nodename, current, NodeLockDCU)
				return &pluginapi.AllocateResponse{}, fmt.Errorf("invalid request device uuid %s", val.UUID)
			}

			deviceInfo, ok := p.HAMiDCUs[val.UUID+"-fake-0"]
			if !ok {
				glog.Errorf("Device serial number %s not found in mapper", devSerialNumber)
				util.PodAllocationFailed(nodename, current, NodeLockDCU)
				return &pluginapi.AllocateResponse{}, fmt.Errorf("device serial number %s not found in mapper", devSerialNumber)
			}

			//Get render and card index path
			cardAndRenderNames, err := util.GetCardAndRender(deviceInfo.PciBusNumber)
			if err != nil {
				glog.Errorf("Device Card and Render Found Error by BUS id %s, Error:%v", deviceInfo.PciBusNumber, err)
				util.PodAllocationFailed(nodename, current, NodeLockDCU)
				return &pluginapi.AllocateResponse{}, fmt.Errorf("device Card and Render Found Error by BUS id %s, Error:%v", deviceInfo.PciBusNumber, err)
			}
			for _, devPath := range cardAndRenderNames {
				devCardPath := "/dev/dri/" + devPath
				devCard := new(pluginapi.DeviceSpec)
				devCard.HostPath = devCardPath
				devCard.ContainerPath = devCardPath
				devCard.Permissions = "rw"
				car.Devices = append(car.Devices, devCard)
			}

			physicalDeviceInfo := p.HAMiDCUs["DCU-"+devSerialNumber+"-fake-0"]

			if val.Usedcores == 100 && val.Usedmem == int32(physicalDeviceInfo.MemoryTotal/1024/1024) {
				_, _ = p.CreateMarkFile(current, &current.Spec.Containers[ctrIndex], physicalDeviceInfo.DvInd, -1)
			}
		}

		//Create virtual DCU and  Make Resource Mark file
		physicalDeviceInfo := p.HAMiDCUs["DCU-"+devSerialNumber+"-fake-0"]
		glog.V(3).Infoln("devreqs=", len(devreq), "usedmem=", devreq[0].Usedmem, ":", physicalDeviceInfo.MemoryTotal/1024/1024)
		if len(devreq) < 2 && devreq[0].Usedmem < int32(physicalDeviceInfo.MemoryTotal/1024/1024) {
			actualCores := int(math.Ceil(float64(devreq[0].Usedcores) * float64(physicalDeviceInfo.ComputeUnit) / 100.0))
			if actualCores < 1 {
				actualCores = 1
			}
			vIdx, err := dcgm.CreateVDevices(physicalDeviceInfo.DvInd, 1, []int{actualCores}, []int{int(devreq[0].Usedmem)})
			if err != nil {
				util.PodAllocationFailed(nodename, current, NodeLockDCU)
				return &responses, err
			}
			markFile, err := p.CreateMarkFile(current, &current.Spec.Containers[ctrIndex], physicalDeviceInfo.DvInd, vIdx[0])
			if len(markFile) > 0 {
				car.Mounts = append(car.Mounts, &pluginapi.Mount{
					ContainerPath: VIRTUAL_DCU_CONF_DIR + fmt.Sprintf("docker/vdev%d.conf", vIdx[0]),
					HostPath:      VIRTUAL_DCU_CONF_DIR + fmt.Sprintf("vdev%d.conf", vIdx[0]),
					ReadOnly:      true,
				}, &pluginapi.Mount{
					ContainerPath: "/opt/hyhal",
					HostPath:      "/opt/hyhal",
					ReadOnly:      true,
				})
			}
		}
		responses.ContainerResponses = append(responses.ContainerResponses, &car)
	}
	car.Mounts = append(car.Mounts, &pluginapi.Mount{
		ContainerPath: "/opt/hyhal",
		HostPath:      "/opt/hyhal",
		ReadOnly:      true,
	})
	glog.V(3).Infoln("response=", responses)
	_ = util.DeleteContainerIndexAnnotations(current)
	util.PodAllocationTrySuccess(nodename, util.HygonDCUDevice, NodeLockDCU, current)
	return &responses, nil
}

func (p *DevicePlugin) AllocateCores(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	var car pluginapi.ContainerAllocateResponse
	responses := pluginapi.AllocateResponse{}

	car = pluginapi.ContainerAllocateResponse{}
	addCommonRWMDevices(&car)

	responses.ContainerResponses = append(responses.ContainerResponses, &car)

	glog.V(3).Infoln("response=", responses)
	return &responses, nil
}

// DCULister Lister serves as an interface between imlementation and Manager machinery. User passes
// implementation of this interface to NewManager function. Manager will use it to obtain resource
// namespace, monitor available resources and instantate a new plugin for them.
type DCULister struct {
	ResUpdateChan            chan dpm.PluginNameList
	Heartbeat                chan bool
	Signal                   chan os.Signal
	ResourceRegisterStrategy chan string
}

// GetResourceNamespace must return namespace (vendor ID) of implemented Lister. e.g. for
// resources in format "color.example.com/<color>" that would be "color.example.com".
func (l *DCULister) GetResourceNamespace() string {
	return "hygon.com"
}

// Discover notifies manager with a list of currently available resources in its namespace.
// e.g. if "color.example.com/red" and "color.example.com/blue" are available in the system,
// it would pass PluginNameList{"red", "blue"} to given channel. In case list of
// resources is static, it would use the channel only once and then return. In case the list is
// dynamic, it could block and pass a new list each times resources changed. If blocking is
// used, it should check whether the channel is closed, i.e. Discover should stop.
func (l *DCULister) Discover(pluginListCh chan dpm.PluginNameList) {
	for {
		select {
		case newResourcesList := <-l.ResUpdateChan: // New resources found
			pluginListCh <- newResourcesList
		case <-pluginListCh: // Stop message received
			// Stop resourceUpdateCh
			return
		}
	}
}

// NewPlugin instantiates a plugin implementation. It is given the last name of the resource,
// e.g. for resource name "color.example.com/red" that would be "red". It must return valid
// implementation of a PluginInterface.
func (l *DCULister) NewPlugin(resourceLastName string) dpm.PluginInterface {
	options := []DevicePluginOption{
		WithHeartbeat(l.Heartbeat),
		WithResource(resourceLastName),
	}
	return NewDevicePlugin(options...)
}
