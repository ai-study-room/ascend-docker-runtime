/**
 * Copyright (c) Huawei Technologies Co., Ltd. 2020-2022. All rights reserved.
 * Description: ascend-docker-runtime工具，配置容器挂载Ascend NPU设备
 */
package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"main/dcmi"
	"os"
	"os/exec"
	"path"
	"strings"
	"syscall"

	"github.com/opencontainers/runtime-spec/specs-go"

	"mindxcheckutils"
)

const (
	loggingPrefix       = "ascend-docker-runtime"
	hookCli             = "ascend-docker-hook"
	destroyHookCli      = "ascend-docker-destroy"
	hookDefaultFilePath = "/usr/local/bin/ascend-docker-hook"
	dockerRuncFile      = "docker-runc"
	runcFile            = "runc"
)

var (
	hookCliPath     = hookCli
	hookDefaultFile = hookDefaultFilePath
	dockerRuncName  = dockerRuncFile
	runcName        = runcFile
)

type args struct {
	bundleDirPath string
	cmd           string
}

func getArgs() (*args, error) {
	args := &args{}

	for i, param := range os.Args {
		if param == "--bundle" || param == "-b" {
			if len(os.Args)-i <= 1 {
				return nil, fmt.Errorf("bundle option needs an argument")
			}
			args.bundleDirPath = os.Args[i+1]
		} else if param == "create" {
			args.cmd = param
		}
	}

	return args, nil
}

var execRunc = func() error {
	runcPath, err := exec.LookPath(dockerRuncName)
	if err != nil {
		runcPath, err = exec.LookPath(runcName)
		if err != nil {
			return fmt.Errorf("failed to find the path of runc: %v", err)
		}
	}
	if _, err := mindxcheckutils.FileChecker(runcPath, false, true, false, 0); err != nil {
		return err
	}

	if err = syscall.Exec(runcPath, append([]string{runcPath}, os.Args[1:]...), os.Environ()); err != nil {
		return fmt.Errorf("failed to exec runc: %v", err)
	}

	return nil
}

func addHook(spec *specs.Spec) error {
	currentExecPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot get the path of ascend-docker-runtime: %v", err)
	}

	hookCliPath = path.Join(path.Dir(currentExecPath), hookCli)
	if _, err := mindxcheckutils.FileChecker(hookCliPath, false, true, false, 0); err != nil {
		return err
	}
	if _, err = os.Stat(hookCliPath); err != nil {
		return fmt.Errorf("cannot find ascend-docker-hook executable file at %s: %v", hookCliPath, err)
	}

	if spec.Hooks == nil {
		spec.Hooks = &specs.Hooks{}
	}
	needUpdate := true
	for _, hook := range spec.Hooks.Prestart {
		if strings.Contains(hook.Path, hookCli) {
			needUpdate = false
		}
	}
	if needUpdate {
		spec.Hooks.Prestart = append(spec.Hooks.Prestart, specs.Hook{
			Path: hookCliPath,
			Args: []string{hookCliPath},
		})
	}

	vdevice, err := dcmi.CreateVDevice(&dcmi.NpuWorker{}, spec)

	if err != nil {
		return err
	}

	if vdevice.VdeviceID != -1 {
		updateEnvAndPostHook(spec, vdevice)
	}

	return nil
}

func updateEnvAndPostHook(spec *specs.Spec, vdevice dcmi.VDeviceInfo) {
	newEnv := make([]string, 0)
	needAddVirtualFlag := true
	for _, line := range spec.Process.Env {
		words := strings.Split(line, "=")
		const LENGTH int = 2
		if len(words) == LENGTH && strings.TrimSpace(words[0]) == "ASCEND_VISIBLE_DEVICES" {
			newEnv = append(newEnv, fmt.Sprintf("ASCEND_VISIBLE_DEVICES=%d", vdevice.VdeviceID))
			continue
		}
		if len(words) == LENGTH && strings.TrimSpace(words[0]) == "ASCEND_RUNTIME_OPTIONS" {
			needAddVirtualFlag = false
			if strings.Contains(words[1], "VIRTUAL") {
				newEnv = append(newEnv, line)
				continue
			} else {
				newEnv = append(newEnv, strings.TrimSpace(line)+",VIRTUAL")
				continue
			}
		}
		newEnv = append(newEnv, line)
	}
	if needAddVirtualFlag {
		newEnv = append(newEnv, fmt.Sprintf("ASCEND_RUNTIME_OPTIONS=VIRTUAL"))
	}
	spec.Process.Env = newEnv
	if currentExecPath, err := os.Executable(); err == nil {
		postHookCliPath := path.Join(path.Dir(currentExecPath), destroyHookCli)
		spec.Hooks.Poststop = append(spec.Hooks.Poststop, specs.Hook{
			Path: postHookCliPath,
			Args: []string{postHookCliPath, fmt.Sprintf("%d", vdevice.CardID), fmt.Sprintf("%d", vdevice.DeviceID),
				fmt.Sprintf("%d", vdevice.VdeviceID)},
		})
	}
}

func modifySpecFile(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("spec file doesnt exist %s: %v", path, err)
	}
	if _, err := mindxcheckutils.FileChecker(path, false, true, true, 0); err != nil {
		return err
	}

	jsonFile, err := os.OpenFile(path, os.O_RDWR, stat.Mode())
	if err != nil {
		return fmt.Errorf("cannot open oci spec file %s: %v", path, err)
	}

	defer jsonFile.Close()

	jsonContent, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		return fmt.Errorf("failed to read oci spec file %s: %v", path, err)
	}

	var spec specs.Spec
	if err := json.Unmarshal(jsonContent, &spec); err != nil {
		return fmt.Errorf("failed to unmarshal oci spec file %s: %v", path, err)
	}

	if err := addHook(&spec); err != nil {
		return fmt.Errorf("failed to inject hook: %v", err)
	}

	jsonOutput, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("failed to marshal OCI spec file: %v", err)
	}

	if _, err := jsonFile.WriteAt(jsonOutput, 0); err != nil {
		return fmt.Errorf("failed to write OCI spec file: %v", err)
	}

	return nil
}

func doProcess() error {
	args, err := getArgs()
	if err != nil {
		return fmt.Errorf("failed to get args: %v", err)
	}

	if args.cmd != "create" {
		return execRunc()
	}

	if args.bundleDirPath == "" {
		args.bundleDirPath, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working dir: %v", err)
		}
	}

	specFilePath := args.bundleDirPath + "/config.json"

	if err := modifySpecFile(specFilePath); err != nil {
		return fmt.Errorf("failed to modify spec file %s: %v", specFilePath, err)
	}

	return execRunc()
}

func main() {
	log.SetPrefix(loggingPrefix)
	if err := doProcess(); err != nil {
		log.Fatal(err)
	}
}
