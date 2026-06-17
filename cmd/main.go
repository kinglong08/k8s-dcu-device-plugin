/*
 * SPDX-License-Identifier: Apache-2.0
 * Copyright 2026 Hygon Information Technology Co., Ltd.
 */

// Kubernetes (k8s) device plugin to enable registration of DCU to a container cluster
package main

import (
	"flag"
	"g.sugon.com/das/dcgm-dcu/pkg/dcgm"
	"github.com/golang/glog"
	"github.com/kubevirt/device-plugin-manager/pkg/dpm"
	"github.com/urfave/cli/v2"
	"k8s-dcu-device-plugin-v2/internal/pkg/plugin"
	"k8s-dcu-device-plugin-v2/internal/pkg/util"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"os"
	"time"
)

var pulse int
var resourceRegisterStrategy, policy string
var version = ""
var resource_mutiple bool

func startDevicePlugin(resources []string) {
	l := plugin.DCULister{
		ResUpdateChan: make(chan dpm.PluginNameList),
		Heartbeat:     make(chan bool),
	}
	manager := dpm.NewManager(&l)

	if pulse > 0 {
		go func() {
			glog.V(2).Infof("Heart beating every %d seconds", pulse)
			for {
				time.Sleep(time.Second * time.Duration(pulse))
				l.Heartbeat <- true
			}
		}()
	}

	go func() {
		// /sys/class/kfd only exists if DCU kernel/driver is installed
		var path = "/sys/class/kfd"
		if _, err := os.Stat(path); err == nil {
			if err != nil {
				glog.Errorf("Error occured: %v", err)
				os.Exit(1)
			}
			if len(resources) > 0 {
				l.ResUpdateChan <- resources
			}
		}
	}()
	manager.Run()
}

func start() {

	glog.V(2).Infof("🚀 🚀 🚀  Hygon DCU Device Plugin start ...")

	glog.V(2).Infof("Init DCU DCGM: %v \n", dcgm.Init())
	defer func() {
		err := dcgm.ShutDown()
		if err != nil {
			glog.Errorf("Hygon DCU Device Plugin Shutdown Error: %v ", err)
			return
		}
	}()

	resourceNamePrefix := util.GetResourceNamePrefix(policy)

	if resourceRegisterStrategy == "dcu" {
		// Run hygon.com/dcu Device Plugin Only
		go startDevicePlugin([]string{resourceNamePrefix})

	} else if resourceRegisterStrategy == "mig" {
		// Run hygon.com/dcu-mig-* Device Plugin Only
		allMigDCUs := util.GetAllMigDCUs()
		for resourceName := range allMigDCUs {
			go startDevicePlugin([]string{resourceNamePrefix + "-" + resourceName})
		}

	} else if resourceRegisterStrategy == "vdcu" {
		// Run hygon.com/dcu-share-* Device Plugin Only
		allVirtualDCUs := util.GetAllVirtualDCUs()
		for resourceName := range allVirtualDCUs {
			go startDevicePlugin([]string{resourceNamePrefix + "-" + resourceName})
		}

	} else if resourceRegisterStrategy == "mixed" {
		// Run hygon.com/dcu Device Plugin
		go startDevicePlugin([]string{resourceNamePrefix})

		// Run hygon.com/dcu-share-* Device Plugin
		allVirtualDCUs := util.GetAllVirtualDCUs()
		for resourceName := range allVirtualDCUs {
			go startDevicePlugin([]string{resourceNamePrefix + "-" + resourceName})
		}

	} else if resourceRegisterStrategy == "hami" {
		// Run hygon.com/dcunum Device Plugin
		go startDevicePlugin([]string{util.GetHAMiResourceName(policy)})

		if resource_mutiple {
			// Run hygon.com/dcucores Device Plugin
			go startDevicePlugin([]string{strings.ReplaceAll(util.GetHAMiResourceName(policy), "dcunum", "dcucores")})
			// Run hygon.com/dcumem Device Plugin
			go startDevicePlugin([]string{strings.ReplaceAll(util.GetHAMiResourceName(policy), "dcunum", "dcumem")})
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	for {
		select {
		case <-sig:
			glog.Info("Received signal, exiting")
			os.Exit(1)
		}
	}

}

func main() {
	c := cli.NewApp()
	c.Name = "Hygon DCU Device Plugin"
	c.Usage = "Hygon DCU device plugin for Kubernetes"
	c.Version = version

	c.Before = func(c *cli.Context) error {
		_ = flag.Set("v", strconv.Itoa(c.Int("log-verbose")))
		_ = flag.Set("stderrthreshold", c.String("stderrthreshold"))
		_ = flag.Set("alsologtostderr", c.String("alsologtostderr"))

		_ = os.Setenv(util.RESOURCE_REGISTER_STRATEGY, resourceRegisterStrategy)
		_ = os.Setenv("POLICY", policy)
		return nil
	}

	c.Action = func(ctx *cli.Context) error {
		start()
		return nil
	}

	c.Flags = []cli.Flag{
		&cli.IntFlag{
			Name:        "pulse",
			Value:       30,
			Usage:       "the device health check intervals",
			Destination: &pulse,
			EnvVars:     []string{"PULSE"},
		},
		&cli.StringFlag{
			Name:        "node-name",
			Usage:       "nodeName in k8s cluster",
			Value:       "0",
			Destination: &util.NodeName,
			EnvVars:     []string{"NODE_NAME"},
		},
		&cli.StringFlag{
			Name:        "strategy",
			Value:       "mixed",
			Destination: &resourceRegisterStrategy,
			Usage:       "the desired strategy for exposing DCU/vDCU/MIG devices on DCUs that support it:\n\t\t[dcu | vdcu | mig | mixed | hami], default value is mixed",
			EnvVars:     []string{"RESOURCE_REGISTER_STRATEGY"},
		},
		&cli.StringFlag{
			Name:        "policy",
			Usage:       "resource name registration policy :\n\t\t[0 | 1 | 2]",
			Value:       "0",
			Destination: &policy,
			EnvVars:     []string{"POLICY"},
		},
		&cli.BoolFlag{
			Name:        "topology-register",
			Usage:       "DCU topology detail register or not:\n\t\t[false | true]",
			Value:       true,
			Destination: &plugin.TopologyRegister,
			EnvVars:     []string{"TOPOLOGY_REGISTER"},
		},
		&cli.StringFlag{
			Name:    "stderrthreshold",
			Usage:   "log threshold that support:\n\t\t[INFO | WARNING | ERROR]",
			Value:   "INFO",
			EnvVars: []string{"LOG_THRESHOLD"},
		},
		&cli.IntFlag{
			Name:    "log-verbose",
			Usage:   "detailed log level support it:\n\t\t(0-10)",
			Value:   2,
			EnvVars: []string{"LOG_VERBOSE"},
		},
		&cli.BoolFlag{
			Name:    "alsologtostderr",
			Usage:   "log outputLog output support it:\n\t\t[false | true]",
			Value:   true,
			EnvVars: []string{"LOG_OUTPUT"},
		},
		&cli.BoolFlag{
			Name:        "resource-multiple",
			Usage:       "hygon.com/dcucores and hygon.com/dcumem resources register or not:\n\t\t[false | true]",
			Value:       false,
			Destination: &resource_mutiple,
			EnvVars:     []string{"RESOURCE_MULTIPLE"},
		},
	}

	err := c.Run(os.Args)
	if err != nil {
		glog.Error(err)
		os.Exit(1)
	}
}
