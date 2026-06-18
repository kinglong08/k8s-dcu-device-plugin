/*
 * SPDX-License-Identifier: Apache-2.0
 * Copyright 2026 Hygon Information Technology Co., Ltd.
 */

package plugin

import (
	"fmt"
	"github.com/HYGON-AI/dcu-exporter-v2/internal/pkg/api"
	"github.com/HYGON-AI/dcu-exporter-v2/internal/pkg/util"
	"github.com/HYGON-AI/dcu-exporter-v2/internal/pkg/util/client"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/HYGON-AI/dcu-dcgm/pkg/dcgm"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	kubeletdevicepluginv1beta1 "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const VIRTUAL_DCU_CONF_DIR = "/etc/vdev/"

var TopologyRegister bool

type DevListFunc func() []*kubeletdevicepluginv1beta1.Device

func (p *DevicePlugin) apiDevices() (*[]*api.DeviceInfo, error) {
	res := []*api.DeviceInfo{}

	glog.V(3).Infof("Found physical %d DCU", len(p.DCUs))

	for _, val := range p.DCUs {
		health := util.SimpleHealthCheck(val.DvInd)

		numas, err := dcgm.ShowNumaTopology([]int{val.DvInd})
		glog.V(3).Infof("Watching DCU with Index: %s NUMA Node: %+v", val.DeviceId, numas)
		if err != nil {
			glog.Errorf("Get DCU numa info error: %v", err)
			continue
		}
		if val.MemoryTotal > 0 {
			res = append(res, &api.DeviceInfo{
				Index:   val.DvInd,
				Id:      "DCU-" + val.DeviceId,
				Count:   4,
				Devmem:  int32(val.MemoryTotal / 1024 / 1024),
				Devcore: 100,
				Numa:    numas[0].NumaNode,
				Type:    "DCU-" + val.DevTypeName,
				Health:  health,
			})
		}
	}
	return &res, nil
}

func (p *DevicePlugin) apiDevicesRemain() (*[]*api.DeviceInfo, error) {
	res := []*api.DeviceInfo{}

	glog.V(3).Infof("Found physical %d DCU", len(p.DCUs))

	for _, val := range p.DCUs {
		health := util.SimpleHealthCheck(val.DvInd)

		numas, err := dcgm.ShowNumaTopology([]int{val.DvInd})
		glog.V(3).Infof("Watching DCU with Index: %s NUMA Node: %+v", val.DeviceId, numas)
		if err != nil {
			glog.Errorf("Get DCU numa info error: %v", err)
			continue
		}

		remainCU, remainMem, _ := dcgm.DeviceRemainingInfo(val.DvInd)
		vdeviceCount, _, _ := dcgm.VDeviceByDvInd(val.DvInd)

		if val.MemoryTotal > 0 {
			res = append(res, &api.DeviceInfo{
				Index:   val.DvInd,
				Id:      "DCU-" + val.DeviceId,
				Count:   int32(4 - vdeviceCount),
				Devmem:  int32(remainMem / 1024 / 1024),
				Devcore: int32((float64(remainCU) / val.ComputeUnit) * 100),
				Numa:    numas[0].NumaNode,
				Type:    "DCU-" + val.DevTypeName,
				Health:  health,
			})
		}
	}
	return &res, nil
}

func (p *DevicePlugin) RegistrInAnnotation() error {
	devices, err := p.apiDevices()
	annos := make(map[string]string)
	if len(util.NodeName) == 0 {
		util.NodeName = os.Getenv(util.NodeNameEnvName)
	}
	node, err := util.GetNode(util.NodeName)
	if err != nil {
		glog.Errorln("get node error", err.Error())
		return err
	}
	encodeddevices := util.EncodeNodeDevices(*devices)
	annos[util.HandshakeAnnosString] = "Reported " + time.Now().String()
	annos[util.RegisterAnnos] = encodeddevices
	glog.V(3).Infof("Reporting devices %s in %v", encodeddevices, time.Now())

	remainDevices, _ := p.apiDevicesRemain()
	encodedRemainDevices := util.EncodeNodeDevices(*remainDevices)
	annos[util.HygonRegisterAnnos] = encodedRemainDevices

	err = util.PatchNodeAnnotations(node, annos)

	if err != nil {
		glog.Errorln("patch node error", err.Error())
	}
	return err
}

func (p *DevicePlugin) WatchAndRegister() {
	glog.Info("into WatchAndRegister")
	p.DCUs = util.GetAllPhysicalDevices()
	for {
		err := p.RegistrInAnnotation()
		_ = p.RefreshContainerDevices()
		if TopologyRegister {
			_ = p.UpdateTopologyConfigMap()
		}
		if err != nil {
			glog.Errorf("register error, %v", err)
			time.Sleep(time.Second * 5)
		} else {
			time.Sleep(time.Second * 30)
		}
	}
}

func (p *DevicePlugin) RefreshContainerDevices() error {
	files, err := os.ReadDir(VIRTUAL_DCU_CONF_DIR + "dynamic/")
	if err != nil {
		return err
	}

	fieldSelector := fields.OneTermEqualSelector("spec.nodeName", util.NodeName).String()
	options := metav1.ListOptions{}
	options.FieldSelector = fieldSelector
	pods, err := client.GetClient().CoreV1().Pods("").List(context.Background(), options)
	if err != nil {
		return err
	}

	for _, f := range files {
		found := false
		for _, val := range pods.Items {
			if strings.Contains(f.Name(), string(val.UID)) && !(val.Status.Phase == corev1.PodSucceeded || val.Status.Phase == corev1.PodFailed) {
				found = true
			}
		}

		if !found {
			var vdidx int
			tmpstr := strings.Split(f.Name(), "_")
			vdidx, _ = strconv.Atoi(tmpstr[3])
			_ = os.RemoveAll(VIRTUAL_DCU_CONF_DIR + "dynamic/" + f.Name())

			var err error
			if vdidx > -1 {
				for try := 0; try < 5; try++ {
					err = dcgm.StopVDevice(vdidx)
					if err == nil || try == 4 {
						glog.V(2).Infof("Stop vDCU %d sucessfully", vdidx)
						for try = 0; try < 5; try++ {
							err = dcgm.DestroySingleVDevice(vdidx)
							if err == nil {
								glog.V(2).Infof("Delete vDCU %d sucessfully", vdidx)
								break
							}
						}
						break
					}
					glog.Errorf("Stop vDCU %d error: %v. Try Again!", vdidx, err)
				}

				_ = os.Remove(fmt.Sprintf(VIRTUAL_DCU_CONF_DIR+"vdev%d.conf", vdidx))
			}

		}
		glog.V(3).Infof("Refresh container file %s.", f.Name())
	}

	for _, val := range pods.Items {
		errorPod := true
		for _, file := range files {
			if strings.Contains(file.Name(), string(val.UID)) {
				errorPod = false
			}
		}
		if util.RequestsVirtualDCU(&val) && errorPod && (val.Status.Phase == corev1.PodRunning || val.Status.Phase == corev1.PodFailed) {
			_ = util.DeletePod(context.Background(), &val)
		}
	}

	return nil
}

func (p *DevicePlugin) informerPodHandler() {
	nodeName := util.NodeName
	stopCh := make(chan struct{})
	fieldSelector := fields.OneTermEqualSelector("spec.nodeName", nodeName).String()

	podInformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				options.FieldSelector = fieldSelector
				return client.GetClient().CoreV1().Pods("").List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				options.FieldSelector = fieldSelector
				return client.GetClient().CoreV1().Pods("").Watch(context.TODO(), options)
			},
		},
		&corev1.Pod{},
		10*time.Minute,
		cache.Indexers{},
	)

	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*corev1.Pod)
			glog.V(3).Infof("[ADD] Pod %s/%s\n", pod.Name, pod.Status.Phase)
			if util.RequestsVirtualDCU(pod) {
				glog.V(3).Infof("[ADD] Pod %s/%s\n", pod.Namespace, pod.Name)
				p.RegistrInAnnotation()
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldPod := oldObj.(*corev1.Pod)
			newPod := newObj.(*corev1.Pod)
			// 监控 Running -> Completed（Succeeded/Failed）
			if oldPod.Status.Phase == corev1.PodRunning &&
				(newPod.Status.Phase == corev1.PodSucceeded || newPod.Status.Phase == corev1.PodFailed) {

				glog.V(3).Infof("[COMPLETE] Pod %s/%s changed from %s -> %s\n",
					newPod.Namespace, newPod.Name, oldPod.Status.Phase, newPod.Status.Phase)
				if util.RequestsVirtualDCU(oldPod) {
					p.RefreshContainerDevices()
					p.RegistrInAnnotation()
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			pod := obj.(*corev1.Pod)
			glog.V(3).Infof("[DELETE] Pod %s/%s\n", pod.Namespace, pod.Name)
			if util.RequestsVirtualDCU(pod) {
				p.RefreshContainerDevices()
				p.RegistrInAnnotation()
			}
		},
	})

	glog.V(2).Infof("Starting pod watcher on node %s...\n", nodeName)
	go podInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, podInformer.HasSynced)

	<-stopCh
}

func (p *DevicePlugin) CreateMarkFile(current *corev1.Pod, ctr *corev1.Container, devidx int, vdevidx int) (string, error) {
	markFile := string(current.UID) + "_" + ctr.Name + "_" + fmt.Sprint(devidx) + "_" + fmt.Sprint(vdevidx)
	cacheFileHostDirectory := fmt.Sprintf(VIRTUAL_DCU_CONF_DIR+"%s", "dynamic")
	_, err := os.Stat(cacheFileHostDirectory)
	if os.IsNotExist(err) {
		err := os.MkdirAll(cacheFileHostDirectory, 0777)
		if err != nil {
			return "", err
		}
		err = os.Chmod(cacheFileHostDirectory, 0777)
		if err != nil {
			return "", err
		}
	}

	err = os.WriteFile(fmt.Sprintf("%s/%s", cacheFileHostDirectory, markFile), []byte(time.DateTime), os.ModePerm)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s", cacheFileHostDirectory, markFile), nil
}

func (p *DevicePlugin) UpdateTopologyConfigMap() error {
	topology, err := dcgm.DiscoverInterconnectTopology()
	if err != nil {
		glog.Errorf("Get DCU topology error: %v", err)
		return err
	}

	glog.V(3).Infof("DCU topology info: %v", topology)
	data, err := json.Marshal(topology)
	if err != nil {
		glog.Errorf("Marshal DCU topology json error: %v", err)
		return err
	}

	patch := map[string]interface{}{
		"data": map[string]string{
			util.NodeName: string(data),
		},
	}
	patchBytes, _ := json.Marshal(patch)

	_, err = client.GetClient().CoreV1().
		ConfigMaps("kube-system").
		Patch(context.Background(), "dcu-topology-info", types.MergePatchType, patchBytes, metav1.PatchOptions{})

	if err == nil {
		return nil
	}

	// if not exists, create it
	if apierrors.IsNotFound(err) {

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.DeviceTopologyConfigMapName,
				Namespace: util.DeviceTopologyConfigMapNamespace,
			},
			Data: map[string]string{
				util.NodeName: string(data),
			},
		}

		_, createErr := client.GetClient().CoreV1().
			ConfigMaps(util.DeviceTopologyConfigMapNamespace).
			Create(context.Background(), cm, metav1.CreateOptions{})

		if createErr == nil {
			return nil
		}

		// if it is creating by other node, try path again
		if apierrors.IsAlreadyExists(createErr) {
			_, retryErr := client.GetClient().CoreV1().
				ConfigMaps(util.DeviceTopologyConfigMapNamespace).
				Patch(context.Background(), util.DeviceTopologyConfigMapName, types.MergePatchType, patchBytes, metav1.PatchOptions{})
			return retryErr
		}

		return createErr
	}

	return err
}
