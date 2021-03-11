package iscsi

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

const tgtAdminBinary = "tgtadm"

// CheckTgtBackingstore checks that tgt-admin supports overlaybd backing store.
func CheckTgtBackingstore(bsName string) error {
	args := []string{
		"--lld", "iscsi",
		"--mode", "system",
		"--op", "show",
	}

	raw, err := exec.Command(tgtAdminBinary, args...).CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to check tgt backing store")
	}

	const bsHeader = "Backing stores:"

	scanner := bufio.NewScanner(strings.NewReader(string(raw)))

	found := false
	for scanner.Scan() {
		part := scanner.Text()
		if part == bsHeader {
			found = true
			break
		}
	}

	if !found {
		return errors.Errorf("unexpected result (from %v) doesn't contain backing store", args)
	}

	for scanner.Scan() {
		part := scanner.Text()
		if len(part) == 0 {
			continue
		}

		// header will be starting with left and make short-circuit
		// break if it is header.
		if part[0] != ' ' {
			break
		}

		part = strings.TrimSpace(part)
		if part == bsName {
			return nil
		}
	}
	return errors.Errorf("tgt-admin doesn't support %s", bsName)
}

// GetISCSIHostSessionMapForTarget returns all the scsi hosts logged into the
// target with given target name and portal.
//
// NOTE: like result from iscsiadm -m node -T $targetName -p ${portal} --rescan
// For example, { 3: [3] }.
func GetISCSIHostSessionMapForTarget(targetIqn string, portal string) (map[int][]int, error) {
	hostSessionIDMap := make(map[int][]int)
	sessionIDHostMap := make(map[int]int)

	sysHostPath := "/sys/class/iscsi_host"
	sysSessionPath := "/sys/class/iscsi_session"
	sysConnectionPath := "/sys/class/iscsi_connection"

	dirs, err := ioutil.ReadDir(sysHostPath)
	if err != nil {
		if os.IsNotExist(err) {
			return hostSessionIDMap, nil
		}
		return nil, err
	}

	for _, dir := range dirs {
		// iSCSI host name is in /sys/class/scsi_host/host%d format.
		// detail in drivers/scsi/hosts.c (linux)
		hostName := dir.Name()
		if !strings.HasPrefix(hostName, "host") {
			continue
		}

		hostNumber, err := strconv.Atoi(strings.TrimPrefix(hostName, "host"))
		if err != nil {
			return nil, errors.Errorf("failed to get number from iSCSI host: %s", hostName)
		}

		deviceDirs, err := ioutil.ReadDir(filepath.Join(sysHostPath, hostName, "device"))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get device from iSCSI host: %s", hostName)
		}

		for _, deviceDir := range deviceDirs {
			// iSCSI session name is in /sys/class/scsi_session/session%u format.
			// detail in drivers/scsi/scsi_transport_iscsi.c (linux)
			sessionName := deviceDir.Name()
			if !strings.HasPrefix(sessionName, "session") {
				continue
			}

			sessionNumber, err := strconv.Atoi(strings.TrimPrefix(sessionName, "session"))
			if err != nil {
				return nil, errors.Errorf("failed to get number from iSCSI session: %s", sessionName)
			}

			if _, ok := sessionIDHostMap[sessionNumber]; ok {
				continue
			}

			// iSCSI session state in [LOGGED_IN, FAILED, FREE]
			// detail in drivers/scsi/scsi_transport_iscsi.c (linux)
			sessionStatePath := filepath.Join(sysSessionPath, sessionName, "state")
			sessionState, err := ioutil.ReadFile(sessionStatePath)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to get session state from %s", sessionStatePath)
			}

			// ignore non-LOGGED_IN session
			if strings.TrimSpace(string(sessionState)) != "LOGGED_IN" {
				continue
			}

			targetNamePath := filepath.Join(sysSessionPath, sessionName, "targetname")
			targetName, err := ioutil.ReadFile(targetNamePath)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to get targetname from session %s", targetNamePath)
			}

			if targetIqn != strings.TrimSpace(string(targetName)) {
				continue
			}

			sessionDeviceDirs, err := ioutil.ReadDir(filepath.Join(sysSessionPath, sessionName, "device"))
			if err != nil {
				return nil, errors.Wrapf(err, "failed to get device from iSCSI session %s", sessionName)
			}

			for _, sdeviceDir := range sessionDeviceDirs {
				// iSCSI connection name is in the format "connection%d:%u"
				// detail in drivers/scsi/scsi_transport_iscsi.c (linux)
				connectionName := sdeviceDir.Name()
				if !strings.HasPrefix(connectionName, "connection") {
					continue
				}

				connectionPath := filepath.Join(sysConnectionPath, connectionName)

				for _, ipPort := range [][]string{
					{"address", "port"},
					{"persistent_address", "persistent_port"},
				} {
					address, err := ioutil.ReadFile(filepath.Join(connectionPath, ipPort[0]))
					if err != nil {
						return nil, errors.Wrapf(err, "failed to get %s from iSCSI connection %s", ipPort[0], connectionPath)
					}

					port, err := ioutil.ReadFile(filepath.Join(connectionPath, ipPort[1]))
					if err != nil {
						return nil, errors.Wrapf(err, "failed to get %s from iSCSI connection %s", ipPort[1], connectionPath)
					}

					if strings.TrimSpace(string(address))+":"+strings.TrimSpace(string(port)) == portal {
						hostSessionIDMap[hostNumber] = append(hostSessionIDMap[hostNumber], sessionNumber)
						sessionIDHostMap[sessionNumber] = hostNumber
						break
					}
				}
			}
		}
	}
	return hostSessionIDMap, nil
}

// GetDevicesForTarget finds the device path with given target name.
//
// NOTE: By default, both targetID and channelID are zero.
func GetDevicesForTarget(targetIqn string, hostNumber, sessionID, channelID, targetID int) ([]string, error) {
	sysSessionPath := "/sys/class/iscsi_session"

	// target name is in the format target%d:%d:%d (hostNumber, channel and target ID number)
	// detail in drivers/scsi/scsi_scan.c (linux)
	targetPath := filepath.Join(sysSessionPath,
		fmt.Sprintf("session%d", sessionID),
		"device",
		fmt.Sprintf("target%d:%d:%d", hostNumber, channelID, targetID))

	dirs, err := ioutil.ReadDir(targetPath)
	if err != nil {
		return nil, errors.Errorf("failed to readdir from iSCSI target: %s", targetPath)
	}

	prefixName := fmt.Sprintf("%d:%d", hostNumber, channelID)
	devices := make([]string, 0)
	for _, dir := range dirs {
		dirName := dir.Name()
		if !strings.HasPrefix(dirName, prefixName) {
			continue
		}

		// TODO(fuweid): check target state is SDEV_RUNNING or not.
		// detail in drivers/scsi/scsi_sysfs.c (linux)
		blockDevicePath := filepath.Join(targetPath, dirName, "block")
		deviceDirs, err := ioutil.ReadDir(blockDevicePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, errors.Errorf("failed to readdir from iSCSI target block: %s", blockDevicePath)
		}

		// FIXME(fuweid): is it possible to have more than one?
		for _, deviceDir := range deviceDirs {
			devices = append(devices, fmt.Sprintf("/dev/%s", deviceDir.Name()))
		}
	}
	return devices, nil
}
