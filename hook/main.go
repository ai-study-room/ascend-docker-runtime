/* Copyright(C) 2022. Huawei Technologies Co.,Ltd. All rights reserved.
   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

// Package main
package main

import (
	"github.com/containerd/containerd/oci"
	securejoin "github.com/cyphar/filepath-securejoin"
	"github.com/opencontainers/runc/libcontainer/utils"
	"golang.org/x/sys/unix"

	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/opencontainers/runtime-spec/specs-go"
	"huawei.com/npu-exporter/v5/common-utils/hwlog"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mindxcheckutils"
)

const (
	loggingPrefix          = "Ascend-kata-hook"
	runLogPath             = "/tmp/hook-run.log"
	ascendRuntimeOptions   = "ASCEND_RUNTIME_OPTIONS"
	ascendRuntimeMounts    = "ASCEND_RUNTIME_MOUNTS"
	ascendVisibleDevices   = "ASCEND_VISIBLE_DEVICES"
	ascendAllowLink        = "ASCEND_ALLOW_LINK"
	ascendDockerCli        = "ascend-docker-cli"
	defaultAscendDockerCli = "/usr/local/bin/ascend-docker-cli"
	configDir              = "/etc/ascend-docker-runtime.d"
	baseConfig             = "base"
	configFileSuffix       = "list"

	kvPairSize       = 2
	maxCommandLength = 65535
)

var (
	containerConfigInputStream = os.Stdin
	doExec                     = syscall.Exec
	ascendDockerCliName        = ascendDockerCli
	defaultAscendDockerCliName = defaultAscendDockerCli
)

var validRuntimeOptions = [...]string{
	"NODRV",
	"VIRTUAL",
}

type containerConfig struct {
	Pid    int
	Rootfs string
	Env    []string
}

func initLogModule(ctx context.Context) error {
	const backups = 2
	const logMaxAge = 365
	runLogConfig := hwlog.LogConfig{
		LogFileName: runLogPath,
		LogLevel:    0,
		MaxBackups:  backups,
		MaxAge:      logMaxAge,
		OnlyToFile:  true,
		FileMaxSize: 2,
	}
	if err := hwlog.InitRunLogger(&runLogConfig, ctx); err != nil {
		fmt.Printf("hwlog init failed, error is %v", err)
		return err
	}
	return nil
}

func parseMounts(mounts string) []string {
	if mounts == "" {
		return []string{baseConfig}
	}
	const maxMountLength = 128
	if len(mounts) > maxMountLength {
		return []string{baseConfig}
	}

	mountConfigs := make([]string, 0)
	for _, m := range strings.Split(mounts, ",") {
		m = strings.TrimSpace(m)
		m = strings.ToLower(m)
		mountConfigs = append(mountConfigs, m)
	}

	return mountConfigs
}

func isRuntimeOptionValid(option string) bool {
	for _, validOption := range validRuntimeOptions {
		if option == validOption {
			return true
		}
	}

	return false
}

func parseRuntimeOptions(runtimeOptions string) ([]string, error) {
	parsedOptions := make([]string, 0)

	if runtimeOptions == "" {
		return parsedOptions, nil
	}
	const maxLength = 128
	if len(runtimeOptions) > maxLength {
		return nil, fmt.Errorf("invalid runtime option")
	}

	for _, option := range strings.Split(runtimeOptions, ",") {
		option = strings.TrimSpace(option)
		if !isRuntimeOptionValid(option) {
			return nil, fmt.Errorf("invalid runtime option")
		}

		parsedOptions = append(parsedOptions, option)
	}

	return parsedOptions, nil
}

func parseSoftLinkMode(allowLink string) (string, error) {
	if allowLink == "True" {
		return "True", nil
	}
	if allowLink == "" || allowLink == "False" {
		return "False", nil
	}

	return "", fmt.Errorf("invalid soft link option")
}

func parseOciSpecFile(file string) (*specs.Spec, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("failed to open the OCI config file: %s", file)
	}
	defer f.Close()

	spec := new(specs.Spec)
	if err := json.NewDecoder(f).Decode(spec); err != nil {
		return nil, fmt.Errorf("failed to parse OCI config file: %s, caused by: %v", file, err)
	}

	if spec.Process == nil {
		return nil, fmt.Errorf("invalid OCI spec for empty process")
	}

	if spec.Root == nil {
		return nil, fmt.Errorf("invalid OCI spec for empty root")
	}

	return spec, nil
}

var getContainerConfig = func() (*containerConfig, error) {
	state := new(specs.State)
	decoder := json.NewDecoder(containerConfigInputStream)

	if err := decoder.Decode(state); err != nil {
		return nil, fmt.Errorf("failed to parse the container's state")
	}

	configPath := path.Join(state.Bundle, "config.json")
	if _, err := mindxcheckutils.RealFileChecker(configPath, true, true, mindxcheckutils.DefaultSize); err != nil {
		return nil, err
	}

	ociSpec, err := parseOciSpecFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse OCI spec: %v", err)
	}
	if len(ociSpec.Process.Env) > maxCommandLength {
		return nil, fmt.Errorf("too many items in spec file")
	}
	// when use ctr->containerd. the rootfs in config.json is a relative path
	rfs := ociSpec.Root.Path
	if !filepath.IsAbs(rfs) {
		rfs = path.Join(state.Bundle, ociSpec.Root.Path)
	}

	ret := &containerConfig{
		Pid:    state.Pid,
		Rootfs: rfs,
		Env:    ociSpec.Process.Env,
	}

	return ret, nil
}

func getValueByKey(data []string, name string) string {
	for _, s := range data {
		p := strings.SplitN(s, "=", 2)
		if len(p) != kvPairSize {
			log.Panicln("environment error")
		}

		if p[0] == name && len(p) == kvPairSize {
			return p[1]
		}
	}

	return ""
}

func readMountConfig(dir string, name string) ([]string, []string, error) {
	configFileName := fmt.Sprintf("%s.%s", name, configFileSuffix)
	baseConfigFilePath, err := filepath.Abs(filepath.Join(dir, configFileName))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to assemble base config file path: %v", err)
	}

	fileInfo, err := os.Stat(baseConfigFilePath)
	if _, err := mindxcheckutils.RealFileChecker(baseConfigFilePath, true, false,
		mindxcheckutils.DefaultSize); err != nil {
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, fmt.Errorf("cannot stat base configuration file %s : %v", baseConfigFilePath, err)
	}

	if !fileInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("base configuration file damaged because is not a regular file")
	}

	f, err := os.Open(baseConfigFilePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open base configuration file %s: %v", baseConfigFilePath, err)
	}
	defer f.Close()

	fileMountList, dirMountList := make([]string, 0), make([]string, 0)
	const maxEntryNumber = 128
	entryCount := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		mountPath := scanner.Text()
		entryCount = entryCount + 1
		if entryCount > maxEntryNumber {
			return nil, nil, fmt.Errorf("mount list too long")
		}
		absMountPath, err := filepath.Abs(mountPath)
		if err != nil {
			continue // skipping files/dirs with any problems
		}
		mountPath = absMountPath

		stat, err := os.Stat(mountPath)
		if err != nil {
			continue // skipping files/dirs with any problems
		}

		if stat.Mode().IsRegular() {
			fileMountList = append(fileMountList, mountPath)
		} else if stat.Mode().IsDir() {
			dirMountList = append(dirMountList, mountPath)
		}
	}

	return fileMountList, dirMountList, nil
}

func readConfigsOfDir(dir string, configs []string) ([]string, []string, error) {
	fileInfo, err := os.Stat(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot stat configuration directory %s : %v", dir, err)
	}

	if !fileInfo.Mode().IsDir() {
		return nil, nil, fmt.Errorf("%s should be a dir for ascend docker runtime, but now it is not", dir)
	}

	fileMountList := make([]string, 0)
	dirMountList := make([]string, 0)

	for _, config := range configs {
		fileList, dirList, err := readMountConfig(dir, config)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to process config %s: %v", config, err)
		}

		fileMountList = append(fileMountList, fileList...)
		dirMountList = append(dirMountList, dirList...)
	}

	return fileMountList, dirMountList, nil
}

func getArgs(cliPath string, containerConfig *containerConfig, fileMountList []string,
	dirMountList []string, allowLink string) []string {
	args := append([]string{cliPath},
		"--allow-link", allowLink, "--pid", fmt.Sprintf("%d", containerConfig.Pid),
		"--rootfs", containerConfig.Rootfs)
	for _, filePath := range fileMountList {
		args = append(args, "--mount-file", filePath)
	}
	for _, dirPath := range dirMountList {
		args = append(args, "--mount-dir", dirPath)
	}
	return args
}

func doPrestartHook() error {
	containerConfig, err := getContainerConfig()
	if err != nil {
		return fmt.Errorf("failed to get container config: %#v", err)
	}

	if visibleDevices := getValueByKey(containerConfig.Env, ascendVisibleDevices); visibleDevices == "" {
		hwlog.RunLog.Infof("Ascend-kata-hook: hasn't ascend device: %#v", ascendVisibleDevices)
		return nil
	}

	hwlog.RunLog.Infof("Ascend-kata-hook: has ascend device define: %#v", ascendVisibleDevices)
	if err := setEnv(*containerConfig); err != nil {
		return err
	}
	if err := mountDev(*containerConfig); err != nil {
		return err
	}

	mountConfigs := parseMounts(getValueByKey(containerConfig.Env, ascendRuntimeMounts))

	fileMountList, dirMountList, err := readConfigsOfDir(configDir, mountConfigs)
	if err != nil {
		return fmt.Errorf("failed to read configuration from config directory: %#v", err)
	}

	for _, file := range fileMountList {
		if _, err := os.Stat(file); err != nil {
			return fmt.Errorf("mount file %s doesn't exist on host", file)
		}

		dest, err := securejoin.SecureJoin(containerConfig.Rootfs, file)
		if err != nil {
			return fmt.Errorf("join file parent: %s, child: %s, with err %v", containerConfig.Rootfs, file, err)
		}
		err = bindMountFile(containerConfig.Rootfs, dest, file)
		if err != nil {
			return fmt.Errorf("bind mount file source: %s, dest: %s with err %v", file, dest, err)
		}
	}

	for _, dir := range dirMountList {
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("mount dir %s doesn't exist on host", dir)
		}

		dest, err := securejoin.SecureJoin(containerConfig.Rootfs, dir)
		if err != nil {
			return fmt.Errorf("join file parent: %s, child: %s with err %v", containerConfig.Rootfs, dir, err)
		}
		err = bindMountDir(containerConfig.Rootfs, dest, dir)
		if err != nil {
			return fmt.Errorf("bind mount dir source: %s, dest: %s with err %v", dir, dest, err)
		}
	}

	return nil
}

func main() {
	defer func() {
		if err := recover(); err != nil {
			log.Fatal(err)
		}
	}()
	log.SetPrefix(loggingPrefix)

	ctx, _ := context.WithCancel(context.Background())
	if err := initLogModule(ctx); err != nil {
		log.Fatal(err)
	}
	logPrefixWords, err := mindxcheckutils.GetLogPrefix()
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := mindxcheckutils.ChangeRuntimeLogMode("hook-run-"); err != nil {
			fmt.Println("defer changeFileMode function failed")
		}
	}()
	hwlog.RunLog.Infof("%v ascend docker hook starting, try to setup container", logPrefixWords)
	if !mindxcheckutils.StringChecker(strings.Join(os.Args, " "), 0,
		maxCommandLength, mindxcheckutils.DefaultWhiteList+" ") {
		hwlog.RunLog.Errorf("%v ascend docker hook failed", logPrefixWords)
		log.Fatal("command error")
	}
	if err := doPrestartHook(); err != nil {
		hwlog.RunLog.Errorf("%v ascend docker hook failed: %#v", logPrefixWords, err)
		log.Fatal(fmt.Errorf("failed in runtime.doProcess: %#v", err))
	}
}

// check if file exist or not
func hasFile(file string, pid int) bool {

	if err := os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"); err != nil {
		hwlog.RunLog.Errorf("set env err:%v", err)
	}

	cmd := exec.Command("nsenter",
		"--target",
		strconv.Itoa(pid),
		"--mount",
		"ls",
		"-l",
		file,
	)
	output, errs := cmd.CombinedOutput()
	if errs != nil {
		hwlog.RunLog.Errorf("Ascend-kata-hook: exec cmd: %s, err: %s ", cmd.String(), output)
		return false
	}
	hwlog.RunLog.Infof("Ascend-kata-hook: hasRootfs ture, return: %s", output)
	return true
}

// createDeviceNode creates the file under /dev in container.
// firstly try to mknod the device.
// bind mount will be executed when mknod in error
func createDeviceNode(rootfs string, dev string, pid int) error {
	device, err := oci.DeviceFromPath(dev)
	if err != nil {
		return err
	}

	//check the rootfs path to verify whether the container is up
	//container is in creating when rootfs exists. should mknod under rootfs.
	//container is running when rootfs doesn't exists. should mknod dev directly
	dest := device.Path
	if hr := hasFile(rootfs, pid); hr {
		dest, err = securejoin.SecureJoin(rootfs, device.Path)

		if err != nil {
			return err
		}
		hwlog.RunLog.Infof("Ascend-kata-hook: rootfs exists, dest is: %s", dest)

	}
	hwlog.RunLog.Infof("Ascend-kata-hook: ----dest----: %s", dest)

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	if err := mknodDeviceNode(dest, *device, pid); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		} else if errors.Is(err, os.ErrPermission) {
			hwlog.RunLog.Infof("Ascend-kata-hook: mknodDevice failed with err:%v bindmount instead", err)
			return bindMountDeviceNode(rootfs, dest, *device)
		}
		return err
	}
	return nil
}

// mknodDevice create the dev file descripter via mknod
// as the hook process is under different  mnt namespace from container process
// mknod should be run via nsenter
// e.g. the container init process pid is 128, the command is:
//
//	nsenter --target 128 --mount mknod /dev/davinci_manager c 245 0
//
// other more, the hook executes before chroot, the dev location
// must be the full path of rootfs
func mknodDeviceNode(dest string, device specs.LinuxDevice, pid int) error {
	if device.Type != "c" {
		return fmt.Errorf("Do not support to mount device type: %s", device.Type)
	}

	//make sure all binaries are under PATH.
	if err := os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"); err != nil {
		hwlog.RunLog.Errorf("set env err:%v", err)
		return err
	}

	devExists := hasFile(dest, pid)
	if devExists {
		hwlog.RunLog.Warnf("Ascend-kata-hook: mknod target already exists.")
		return nil
	}

	cmd := exec.Command("nsenter",
		"--target",
		strconv.Itoa(pid),
		"--mount",
		"mknod",
		dest,
		device.Type,
		strconv.FormatInt(device.Major, 10),
		strconv.FormatInt(device.Minor, 10),
	)
	output, errs := cmd.CombinedOutput()
	if errs != nil {
		hwlog.RunLog.Errorf("Ascend-kata-hook: exec cmd: %s, err: %s ", cmd.String(), output)
		return fmt.Errorf("Ascend-kata-hook: Mknod err: %v", errs)
	}
	return nil
}

// bindMountDeviceNode creates the rootfs/dev/xxx
func bindMountDeviceNode(rootfs string, dest string, device specs.LinuxDevice) error {
	return bindMountFile(rootfs, dest, device.Path)

}

// bindMountFile creates and mount file from host to container
func bindMountFile(rootfs string, dest string, source string) error {
	f, err := os.Create(dest)
	if err != nil && !os.IsExist(err) {
		return err
	}
	if f != nil {
		_ = f.Close()
	}
	return bindMount(rootfs, dest, source)
}

// bindMountDir creates dirctory and mount dir
func bindMountDir(rootfs string, dest string, source string) error {
	err := os.MkdirAll(dest, 0550)
	if err != nil {
		return err
	}
	return bindMount(rootfs, dest, source)
}

// bindMount create the dest via bind mount the host path
func bindMount(rootfs string, dest string, source string) error {
	return utils.WithProcfd(rootfs, dest, func(dstFd string) error {
		if dstFd != "" {
			dest = dstFd
		}
		unix.Mount(source, dest, "bind", unix.MS_BIND, "")
		return nil
	})
}

// mountDeviceManger creates the 910b manager relative device.
// notes: only be tested on 910b chip currently.
// which include davinci_manager, hisi_hdc, devmm_svm
// Args:
//
//	rootfs(string): target container's rootfs path.
//	pid(int): target container's init process id
func mountDeviceManager(rootfs string, pid int) error {
	dev_names := []string{"davinci_manager", "hisi_hdc", "devmm_svm"}

	for _, d := range dev_names {
		hwlog.RunLog.Infof("Ascend-kata-hook: mount dev manager %s with rootfs %s", d, rootfs)
		if err := mountDevice(rootfs, d, pid); err != nil {
			return err
		}
	}
	return nil
}

// mountDevice create the dev file describer for a device
// Args:
//
//	rootfs(string): target container's rootfs path
//	dev(string): the full path of device in container
//	pid(int): target container's init process id
func mountDevice(rootfs string, dev string, pid int) error {
	devfile := path.Join("/dev", dev)
	if _, err := os.Stat(devfile); err != nil {
		hwlog.RunLog.Errorf("Dev %s doesn't exist on host, err: %v", devfile, err)
		return fmt.Errorf("Npu device manager file %s doesn't exist on host", devfile)
	}
	if err := createDeviceNode(rootfs, devfile, pid); err != nil {
		return err
	}
	return nil
}

// mountDev create the following devs under the rootfs/dev
//
//	1)davinci_manager
//	2)hisi_hdc
//	3)devmm_svm
//	4 all the davinci dev like davinci1,davinci2
func mountDev(config containerConfig) error {

	if err := mountDeviceManager(config.Rootfs, config.Pid); err != nil {
		return err
	}
	WAIT_TOTAL_SECONDS, CHECK_PERIOD := 60, 3
	start := time.Now()
	has_dev := false
	for {
		dev_files, err := ioutil.ReadDir("/dev")
		if err != nil {
			hwlog.RunLog.Errorf("Ascend-kata-hook: get /dev/ error %v", err)
			return err
		}

		for _, dev_file := range dev_files {
			if strings.Contains(dev_file.Name(), "davinci") {
				if dev_file.Name() == "davinci_manager" {
					continue
				}
				has_dev = true
				err := mountDevice(config.Rootfs, dev_file.Name(), config.Pid)
				if err != nil {
					hwlog.RunLog.Errorf("Ascend-kata-hook: mountDevice:%s, error: %v", dev_file.Name(), err)
					return err
				}
			}
			hwlog.RunLog.Infof("Ascend-kata-hook: get dev file %v", dev_file.Name())
		}

		//wait the dev ready
		delta := time.Now().Sub(start)
		if delta > time.Duration(WAIT_TOTAL_SECONDS)*time.Second {
			break
		}
		time.Sleep(time.Duration(CHECK_PERIOD) * time.Second)

	}
	if !has_dev {
		hwlog.RunLog.Errorf("Ascend-kata-hook: timeout to find /dev/davinci*")
	}
	return nil
}

// setEnv export the ascend driver's libs
func setEnv(config containerConfig) error {
	env_file := path.Join(config.Rootfs, "/root/.bashrc")
	f, err := os.OpenFile(env_file, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0660)
	if err != nil {
		hwlog.RunLog.Errorf("Ascend-kata-hook: Cannot open evn file %s, err: %v\n", env_file, err)
		return err
	}
	defer f.Close()
	f.WriteString("export LD_LIBRARY_PATH=/usr/local/dcmi/:/usr/local/Ascend/driver/lib64/common/:/usr/local/Ascend/driver/lib64/driver/")
	return nil
}
