package snapshot

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/continuity"
	dockermount "github.com/docker/docker/pkg/mount"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

const (
	overlaybdCreate = "/opt/overlaybd/bin/overlaybd-create"
)

func overlaybdConfPath(dir string) string {
	return filepath.Join(dir, zdfsMetaDir, "config.v1.json")
}

func overlaybdInitDebuglogPath(dir string) string {
	return filepath.Join(dir, zdfsMetaDir, "init-debug.log")
}

func overlaybdBackstoreMarkFile(dir string) string {
	return filepath.Join(dir, zdfsMetaDir, "backstore_mark")
}

func overlaybdLoopbackDeviceID(id string) string {
	paddings := strings.Repeat("0", 13-len(id))
	return fmt.Sprintf("naa.%d%s%s", obdLoopNaaPrefix, paddings, id)
}

func overlaybdLoopbackDevicePath(id string) string {
	return fmt.Sprintf("/sys/kernel/config/target/loopback/%s", id)
}

func overlaybdLoopbackDeviceLunPath(id string) string {
	return fmt.Sprintf("/sys/kernel/config/target/loopback/%s/tpgt_1/lun/lun_0", id)
}

func overlaybdTargetPath(id string) string {
	return fmt.Sprintf("/sys/kernel/config/target/core/user_%d/dev_%s", obdHbaNum, id)
}

func atomicWriteOverlaybdTargetConfig(dir string, configJSON *OverlayBDBSConfig) error {
	data, err := json.Marshal(configJSON)
	if err != nil {
		return errors.Wrapf(err, "failed to marshal %+v configJSON into JSON", configJSON)
	}

	confPath := overlaybdConfPath(dir)
	if err := continuity.AtomicWriteFile(confPath, data, 0600); err != nil {
		return errors.Wrapf(err, "failed to commit the overlaybd config on %s", confPath)
	}
	return nil
}

func constructOverlayBDWritableSpec(dir, parent string) error {
	configJSON := OverlayBDBSConfig{
		Lowers:     []OverlayBDBSConfigLower{},
		ResultFile: overlaybdInitDebuglogPath(dir),
	}

	parentConfJSON, err := loadBackingStoreConfig(parent)
	if err != nil {
		return err
	}
	rwDir := path.Join(dir, zdfsMetaDir)
	configJSON.RepoBlobURL = parentConfJSON.RepoBlobURL
	configJSON.Lowers = parentConfJSON.Lowers
	configJSON.Upper = OverlayBDBSConfigUpper{
		Index: path.Join(rwDir, idxFile),
		Data:  path.Join(rwDir, dataFile),
	}

	return atomicWriteOverlaybdTargetConfig(dir, &configJSON)
}

//dst is just zdfsMerged dir
func tcmuMount(snID, dir, dst string, lowers []string, mountLabel string, blockMode bool) (devName string, retErr error) {

	starttime := time.Now()

	logrus.Infof("LSMD enter tcmuMount snId %s, dir %s, lowerDirs%s, mountLabel:%s", snID, dir, lowers, mountLabel)
	//first to check if dst is being mounted
	m, err := dockermount.Mounted(dst)
	if err != nil {
		logrus.Errorf("LSMD ERROR leave vrbdMount(dst:%s,lowerDirs%s,mountLabel:%s, ", dst, lowers, mountLabel)
		return "", err
	}
	if m {
		logrus.Warnf("LSMD WARN leave vrbdMount(dst:%s) dst has been mounted.", dst)
		return "", nil
	}
	if blockMode {
		devPathName := path.Join(dst, deviceName)
		if _, err := os.Stat(devPathName); err == nil {
			logrus.Infof("tcmu_path exists: %s", devPathName)
			buf, err := ioutil.ReadFile(devPathName)
			if err != nil {
				logrus.Errorf("read file failed, path: %s, err: %s", devPathName, err.Error())
				return "", err
			}
			devName = string(buf)
			logrus.Infof("device of %s has been created before: %s", dst, devName)
			return devName, nil
		}
	}

	if blockMode {
		if err := createRWlayer(path.Dir(dst), lowers[0], "tcmu"); err != nil {
			return "", err
		}
	}

	targetPath := overlaybdTargetPath(snID)
	err = os.MkdirAll(targetPath, 0700)
	if err != nil {
		return "", errors.Wrapf(err, "failed to create target dir for %s", targetPath)
	}

	defer func() {
		if retErr != nil {
			logrus.Infof("failed tcmuMount, clean, err: %v", retErr)
			killProcess(snID)
			rerr := os.RemoveAll(targetPath)
			if rerr != nil {
				logrus.Warnf("failed to clean target dir %s", targetPath)
			}
		}
	}()

	if err = ioutil.WriteFile(path.Join(targetPath, "control"), ([]byte)(fmt.Sprintf("dev_config=overlaybd/%s;%s", overlaybdConfPath(dir), snID)), 0666); err != nil {
		return "", errors.Wrapf(err, "failed to write target dev_config for %s", targetPath)
	}

	err = ioutil.WriteFile(path.Join(targetPath, "control"), ([]byte)(fmt.Sprintf("max_data_area_mb=%d", obdMaxDataAreaMB)), 0666)
	if err != nil {
		return "", errors.Wrapf(err, "failed to write target max_data_area_mb for %s", targetPath)
	}

	elapsed := float64(time.Since(starttime)) / float64(time.Millisecond)
	logrus.Infof("to prepre time used: %v", elapsed)
	// err = ioutil.WriteFile(path.Join(targetPath, "control"), ([]byte)("nl_reply_supported=-1"), 0666)
	// if err != nil {
	// 	return "", errors.Wrapf(err, "failed to write target max_data_area_mb for %s", targetPath)
	// }

	debugLogPath := overlaybdInitDebuglogPath(dir)
	logrus.Infof("result file: %s", debugLogPath)
	err = os.RemoveAll(debugLogPath)
	if err != nil {
		return "", errors.Wrapf(err, "failed to remote result file for %s", targetPath)
	}

	// 5s
	for retry := 0; retry < 100; retry++ {
		err = ioutil.WriteFile(path.Join(targetPath, "enable"), ([]byte)("1"), 0666)

		if err != nil {
			perror, ok := err.(*os.PathError)
			if ok {
				if perror.Err == syscall.EAGAIN {
					logrus.Infof("write %s returned EAGAIN, retry", targetPath)
					time.Sleep(50 * time.Millisecond)
					continue
				}
			}
			return "", errors.Wrapf(err, "failed to write enable for %s", targetPath)
		} else {
			break
		}
	}
	if err != nil {
		return "", errors.Wrapf(err, "failed to write enable for %s", targetPath)
	}

	// 20s
	// read the init-debug.log for readable
	err = fmt.Errorf("timeout")
	for retry := 0; retry < 1000; retry++ {
		if data, derr := ioutil.ReadFile(debugLogPath); derr == nil {
			if string(data) == "success" {
				err = nil
				break
			} else {
				return "", errors.Errorf("failed to enable target for %s, %s", targetPath, data)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		logrus.Warnf("timeout to start device for snID: %s, lastErr: %v", snID, err)
		return "", errors.Wrapf(err, "failed to enable target for %s", targetPath)
	}

	elapsed = float64(time.Since(starttime)) / float64(time.Millisecond)
	logrus.Infof("to backstore started time used: %v", elapsed)

	loopDevID := overlaybdLoopbackDeviceID(snID)
	loopDevPath := overlaybdLoopbackDevicePath(loopDevID)

	err = os.MkdirAll(loopDevPath, 0700)
	if err != nil {
		return "", errors.Wrapf(err, "failed to create loopback dir %s", loopDevPath)
	}

	tpgtPath := path.Join(loopDevPath, "tpgt_1")
	lunPath := overlaybdLoopbackDeviceLunPath(loopDevID)
	err = os.MkdirAll(lunPath, 0700)
	if err != nil {
		return "", errors.Wrapf(err, "failed to create loopback lun dir %s", lunPath)
	}

	defer func() {
		if retErr != nil {
			rerr := os.RemoveAll(lunPath)
			if rerr != nil {
				logrus.Warnf("failed to clean loopback lun %s, err %v", lunPath, rerr)
			}

			rerr = os.RemoveAll(tpgtPath)
			if rerr != nil {
				logrus.Warnf("failed to clean loopback tpgt %s, err %v", tpgtPath, rerr)
			}

			rerr = os.RemoveAll(loopDevPath)
			if rerr != nil {
				logrus.Warnf("failed to clean loopback dir %s, err %v", loopDevPath, rerr)
			}
		}
	}()

	nexusPath := path.Join(tpgtPath, "nexus")
	err = ioutil.WriteFile(nexusPath, ([]byte)(loopDevID), 0666)
	if err != nil {
		return "", errors.Wrapf(err, "failed to write loopback nexus %s", nexusPath)
	}

	linkPath := path.Join(lunPath, "dev_"+snID)
	err = os.Symlink(targetPath, linkPath)
	if err != nil {
		return "", errors.Wrapf(err, "failed to create loopback link %s", linkPath)
	}

	elapsed = float64(time.Since(starttime)) / float64(time.Millisecond)
	logrus.Infof("to tcm loop started time used: %v", elapsed)

	defer func() {
		if retErr != nil {
			rerr := os.RemoveAll(linkPath)
			if err != nil {
				logrus.Warnf("failed to clean loopback link %s, err %v", linkPath, rerr)
			}
		}
	}()

	devAddressPath := path.Join(tpgtPath, "address")
	bytes, err := ioutil.ReadFile(devAddressPath)
	if err != nil {
		return "", errors.Wrapf(err, "failed to read loopback address for %s", devAddressPath)
	}
	deviceNumber := strings.TrimSuffix(string(bytes), "\n")
	logrus.Infof("get device number %s", deviceNumber)

	// The device doesn't show up instantly. Need retry here.
	var lastErr error = nil
	for retry := 0; retry < maxAttachAttempts; retry++ {
		devDirs, err := ioutil.ReadDir(scsiBlockDevicePath(deviceNumber))
		if err != nil {
			lastErr = err
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if len(devDirs) == 0 {
			lastErr = errors.Errorf("empty device found")
			time.Sleep(5 * time.Millisecond)
			continue
		}

		for _, dev := range devDirs {
			device := fmt.Sprintf("/dev/%s", dev.Name())

			if !blockMode {

				elapsed = float64(time.Since(starttime)) / float64(time.Millisecond)
				logrus.Infof("to device mountable time used: %v", elapsed)

				if err := execCmd("blockdev --setra 8192 " + device); err != nil {
					logrus.Warnf("set ra failed")
				}
				if err := execCmd("blockdev --setfra 8192 " + device); err != nil {
					logrus.Warnf("set ra failed")
				}
				if err := unix.Mount(device, dst, "ext4", unix.MS_RDONLY, ""); err != nil {
					lastErr = errors.Wrapf(err, "failed to mount %s to %s", device, dst)
					time.Sleep(5 * time.Millisecond)
					break // retry
				}
				elapsed = float64(time.Since(starttime)) / float64(time.Millisecond)
				logrus.Infof("to device mounted time used: %v", elapsed)
			} else {
				devPathName := path.Join(dst, deviceName)
				if err := ioutil.WriteFile(devPathName, []byte(device), 0666); err != nil {
					logrus.Errorf("failed to write device path file[%s], dev: %s", devPathName, devName)
					return "", err
				}
			}

			devSavedPath := overlaybdBackstoreMarkFile(dir)
			if err := ioutil.WriteFile(devSavedPath, []byte(device), 0644); err != nil {
				return "", errors.Wrapf(err, "failed to create backstore mark file of snapshot %s", snID)
			}
			logrus.Infof("write device name: %s into file: %s", device, devSavedPath)
			return device, nil
		}
	}
	logrus.Warnf("timeout to find device for snID: %s, lastErr: %v", snID, lastErr)
	return "", lastErr
}

func killProcess(snID string) error {

	cmdLine := "ps -ef | grep -v grep | grep -w 'overlaybd-service " + snID + "'  | awk '{print $2}' "
	cmd := exec.Command("/bin/bash", "-c", cmdLine)
	out, err := cmd.CombinedOutput()
	strVal := strings.Trim(string(out), "\n ")
	logrus.Infof("exec ps snID:%s, err:%v, out:%s", snID, err, strVal)
	// ps找pid，与pidfile对比
	// if strVal != pidstr {
	// 	logrus.Errorf("failed to find overlaybd process, expect pid %s, got %s", pidstr, strVal)
	// 	if strVal != "" {
	// 		// pid不匹配，一定不能杀进程
	// 		return errors.Errorf("failed to find overlaybd process, expect pid %s, got %s", pidstr, strVal)
	// 	}
	// }
	if strVal == "" {
		// 进程不存在，不杀进程，
		logrus.Warnf("overlaybd process not found, snID: %s", snID)
		return nil
	}

	// kill找到的进程（即使与pidfile不匹配？）
	cmdLine = "ps -ef | grep -v grep | grep -w 'overlaybd-service " + snID + "'  | awk '{print $2}' | xargs kill -2"
	cmd = exec.Command("/bin/bash", "-c", cmdLine)
	out, err = cmd.CombinedOutput()
	logrus.Infof("exec kill snID: %s, pid:%s,  err:%v, out:%s", snID, strVal, err, string(out))

	for i := 0; i < 500; i++ {
		cmdLine = "ps -ef | grep -v grep | grep -w 'overlaybd-service " + snID + "' |  wc -l"
		cmd = exec.Command("/bin/bash", "-c", cmdLine)
		out, _ = cmd.CombinedOutput()
		strVal = strings.Trim(string(out), "\n ")
		num, err := strconv.Atoi(strVal)
		if err != nil {
			time.Sleep(20 * time.Microsecond)
			continue
		}
		if num == 0 {
			return nil
		}
		time.Sleep(20 * time.Microsecond)
		// logrus.Infof("wait kill")
	}
	logrus.Warnf("timeout to kill process for snID: %s", snID)
	return errors.Wrapf(err, "failed to kill overlaybd process for snID: %s", snID)
}

func execCmd(str string) error {
	cmd := exec.Command("/bin/bash", "-c", str)
	out, err := cmd.CombinedOutput()
	if err != nil {
		logrus.Errorf("LSMD exec error cmdLind:%s, out:%s, err:%s", str, string(out), err)
	} else {
		logrus.Infof("LSMD exec cmdLind:%s, out:%s", str, string(out))
	}
	return err
}
