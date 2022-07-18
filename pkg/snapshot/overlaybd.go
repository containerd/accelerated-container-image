package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/storage"
	"github.com/containerd/continuity"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

var (
	writeType   int
	zdfsIsReady bool //indicate if zdfs' binaries or rpms are ready
)

const (
	zdfsMetaDir         = "zdfsmeta"               //meta dir that contains the dadi image meta files
	iNewFormat          = ".aaaaaaaaaaaaaaaa.lsmt" //characteristic file of dadi image
	zdfsChecksumFile    = ".checksum_file"         //file containing the checksum data if each dadi layer file to guarantee data consistent
	zdfsOssurlFile      = ".oss_url"               //file containing the address of layer file
	zdfsOssDataSizeFile = ".data_size"             //file containing the size of layer file
	zdfsOssTypeFile     = ".type"                  //file containing the type, such as layern, commit(layer file on local dir), oss(layer file is in oss
	zdfsTrace           = ".trace"

	overlaybdBaseLayer = "/opt/overlaybd/baselayers/.commit"
	ImageRefFile       = "image_ref"        // save cri.imageRef as file for old dadi format
	SandBoxMetaFile    = "pod_sandbox_meta" // for SAE
)

//If error is nil, the existence is valid.
//If error is not nil, the existence is invalid. Can't make sure if path exists.
func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil //path exists.
	}
	if os.IsNotExist(err) {
		return false, nil //pash doen't exist.
	}
	return false, err //can't make sure if path exists.
}

func IsZdfsLayer(dir string) (bool, error) {
	exists, _ := pathExists(overlaybdConfPath(dir))
	if exists {
		return true, nil
	}

	b, err := hasZdfsFlagFiles(path.Join(dir, "fs"))
	if err != nil {
		logrus.Errorf("LSMD ERROR failed to IsZdfsLayerInApplyDiff(dir%s), err:%s", dir, err)
		return false, fmt.Errorf("LSMD ERROR failed to IsZdfsLayerInApplyDiff(dir%s), err:%s", dir, err)
	}
	return b, nil
}

func (o *snapshotter) getSnDir(snID string) string {
	return filepath.Join(o.root, "snapshots", snID)
}

func (o *snapshotter) snapshotterPath(id string) string {
	//info is nil, so return default path
	return filepath.Join(o.root, "snapshots", id, "fs")
}

func (o *snapshotter) convertIDsToDirs(parentIDs []string) []string {
	ret := []string{}
	for _, v := range parentIDs {
		ret = append(ret, filepath.Dir(o.snapshotterPath(v)))
	}
	return ret
}

func overlaybdConfPath(dir string) string {
	return filepath.Join(dir, "block", "config.v1.json")
}

func overlaybdInitDebuglogPath(dir string) string {
	return filepath.Join(dir, zdfsMetaDir, "init-debug.log")
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

func getTrimStringFromFile(filePath string) (string, error) {
	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return strings.Trim(string(data), " \n"), nil
}

func hasZdfsFlagFiles(dir string) (bool, error) {
	fileNames := []string{iNewFormat, zdfsChecksumFile, zdfsOssurlFile, zdfsOssDataSizeFile, zdfsOssTypeFile}
	for _, name := range fileNames {
		fullPath := path.Join(dir, name)
		b, err := pathExists(fullPath)
		if err != nil {
			return false, fmt.Errorf("LSMD ERROR failed to check if %s exists. err:%s", fullPath, err)
		}

		if b == false {
			return false, nil
		}
	}
	return true, nil
}

func (o *snapshotter) MountDadiSnapshot(ctx context.Context, key string, info snapshots.Info, s storage.Snapshot, recordTracePath string) (bool, string, error) {
	//dadi image layer may more than one layer
	if s.Kind != snapshots.KindActive {
		return false, "", nil
	}
	if len(s.ParentIDs) < 1 {
		return false, "", nil
	}

	//check dadi layer
	dir := o.getSnDir(s.ParentIDs[0])
	isDadi, err := IsZdfsLayer(dir)
	if err != nil {
		log.G(ctx).WithError(err).Errorf("[DADI] invalid dadi snapshot as parent: %s", dir)
		return false, "", errors.Wrapf(err, "[DADI] invalid dadi snapshot as parent")
	}
	if !isDadi {
		log.G(ctx).Infof("[DADI]is not dadi image. key:%s, parent:%s", key, dir)
		return false, "", nil
	}
	log.G(ctx).Infof("[DADI] is dadi image. key:%s, parent:%s", key, dir)

	//getLowerDir
	snDir := o.getSnDir(s.ID)

	lowers := o.convertIDsToDirs(s.ParentIDs)
	if err := PrepareMeta(snDir, lowers, info, recordTracePath); err != nil {
		return true, "", err
	}
	return true, "", nil
}

func GetBlobRepoDigest(dir string) (string, string, error) {
	// get repoUrl from .oss_url
	url, err := getTrimStringFromFile(path.Join(dir, zdfsOssurlFile))
	if err != nil {
		return "", "", err
	}

	idx := strings.LastIndex(url, "/")
	if !strings.HasPrefix(url[idx+1:], "sha256") {
		return "", "", fmt.Errorf("Can't parse sha256 from url %s", url)
	}

	return url[0:idx], url[idx+1:], nil
}

func constructImageBlobURL(ref string) (string, error) {
	refspec, err := reference.Parse(ref)
	if err != nil {
		return "", errors.Wrapf(err, "invalid repo url %s", ref)
	}

	host := refspec.Hostname()
	repo := strings.TrimPrefix(refspec.Locator, host+"/")
	return "https://" + path.Join(host, "v2", repo) + "/blobs", nil
}

func GetBlobSize(dir string) (uint64, error) {
	str, err := getTrimStringFromFile(path.Join(dir, zdfsOssDataSizeFile))
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(str, 10, 64)
}

// loadBackingStoreConfig loads overlaybd target config.
func loadBackingStoreConfig(dir string) (*OverlayBDBSConfig, error) {
	confPath := overlaybdConfPath(dir)
	data, err := ioutil.ReadFile(confPath)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read config(path=%s) of snapshot %s", confPath, dir)
	}

	var configJSON OverlayBDBSConfig
	if err := json.Unmarshal(data, &configJSON); err != nil {
		return nil, errors.Wrapf(err, "failed to unmarshal data(%s)", string(data))
	}

	return &configJSON, nil
}

// ConstructOverlayBDSpec generates the config spec for overlaybd target.
func ConstructOverlayBDSpec(dir, parent, repo, digest string, info snapshots.Info, size uint64, recordTracePath string) error {
	configJSON := OverlayBDBSConfig{
		ImageRef:   info.Labels[labelKeyCriImageRef],
		Lowers:     []OverlayBDBSConfigLower{},
		ResultFile: overlaybdInitDebuglogPath(dir),
	}
	configJSON.RepoBlobURL = repo
	if parent == "" {
		configJSON.Lowers = append(configJSON.Lowers, OverlayBDBSConfigLower{
			File: overlaybdBaseLayer,
		})
	} else {
		parentConfJSON, err := loadBackingStoreConfig(parent)
		if err != nil {
			return err
		}
		if repo == "" {
			configJSON.RepoBlobURL = parentConfJSON.RepoBlobURL
		}
		configJSON.Lowers = parentConfJSON.Lowers
	}

	configJSON.RecordTracePath = recordTracePath
	configJSON.Lowers = append(configJSON.Lowers, OverlayBDBSConfigLower{
		Digest: digest,
		Size:   int64(size),
		Dir:    path.Join(dir, "block"),
	})

	return atomicWriteOverlaybdTargetConfig(dir, &configJSON)
}

func updateSpec(dir, recordTracePath string) error {
	bsConfig, err := loadBackingStoreConfig(dir)
	if err != nil {
		return err
	}
	if recordTracePath == bsConfig.RecordTracePath {
		// No need to update
		return nil
	}
	bsConfig.RecordTracePath = recordTracePath
	return atomicWriteOverlaybdTargetConfig(dir, bsConfig)
}

//The idDir must be clean here according to process of createSnapshot(...).
//Used to create necessary dirs and top files for lsmd.
//idDir is just snDir that is only used by the snapshot.
func PrepareMeta(idDir string, lowers []string, info snapshots.Info, recordTracePath string) error {

	makeConfig := func(dir string, parent string) error {
		logrus.Infof("ENTER makeConfig(dir: %s, parent: %s)", dir, parent)
		dstDir := path.Join(dir, "block")

		repo, digest, err := GetBlobRepoDigest(dstDir)
		if err != nil {
			return err
		}

		if imageRef, ok := info.Labels[labelKeyCriImageRef]; ok {
			logrus.Infof("read imageRef from labelKeyCriImageRef: %s", imageRef)
			repo, _ = constructImageBlobURL(imageRef)
		}
		logrus.Infof("construct repoBlobUrl: %s", repo)

		size, err := GetBlobSize(dstDir)
		if err := ConstructOverlayBDSpec(dir, parent, repo, digest, info, size, recordTracePath); err != nil {
			return err
		}
		return nil
	}

	doDir := func(dir string, parent string) error {
		dstDir := path.Join(dir, zdfsMetaDir)
		//1.check if the dir exists. Create the dir only when dir doesn't exist.
		b, err := pathExists(dstDir)
		if err != nil {
			logrus.Errorf("LSMD ERROR PathExists(%s) err:%s", dstDir, err)
			return err
		}

		if b {
			configPath := overlaybdConfPath(dir)
			configExists, err := pathExists(configPath)
			if err != nil {
				logrus.Errorf("LSMD ERROR PathExists(%s) err:%s", configPath, err)
				return err
			}
			if configExists {
				logrus.Infof("%s has been created yet.", configPath)
				return updateSpec(dir, recordTracePath)
			}
			// config.v1.json does not exist, for early pulled layers
			return makeConfig(dir, parent)
		}

		b, _ = pathExists(path.Join(dir, "block", "config.v1.json"))
		if b {
			// is new dadi format
			return nil
		}

		//2.create tmpDir in dir
		tmpDir, err := ioutil.TempDir(dir, "temp_for_prepare_dadimeta")
		if err != nil {
			logrus.Errorf("LSMD ERROR ioutil.TempDir(%s,.) err:%s", dir, err)
			return err
		}

		//3.copy meta files to tmpDir)
		srcDir := path.Join(dir, "fs")
		if err := copyPulledZdfsMetaFiles(srcDir, tmpDir); err != nil {
			logrus.Errorf("failed to copyPulledZdfsMetaFiles(%s, %s), err:%s", srcDir, tmpDir, err)
			return err
		}

		blockDir := path.Join(dir, "block")
		if err := copyPulledZdfsMetaFiles(srcDir, blockDir); err != nil {
			logrus.Errorf("failed to copyPulledZdfsMetaFiles(%s, %s), err:%s", srcDir, blockDir, err)
			return err
		}

		//4.rename tmpDir to zdfsmeta
		if err = os.Rename(tmpDir, dstDir); err != nil {
			return err
		}

		//5.generate config.v1.json
		return makeConfig(dir, parent)
	}

	num := len(lowers)
	parent := ""
	for m := 0; m < num; m++ {
		dir := lowers[num-m-1]
		if err := doDir(dir, parent); err != nil {
			logrus.Errorf("LSMD ERROR doDir(%s) err:%s", dir, err)
			return err
		}
		parent = dir
	}

	return nil
}

func copyPulledZdfsMetaFiles(srcDir, dstDir string) error {
	fileNames := []string{iNewFormat, zdfsChecksumFile, zdfsOssurlFile, zdfsOssDataSizeFile, zdfsOssTypeFile, zdfsTrace}
	for _, name := range fileNames {
		srcPath := path.Join(srcDir, name)
		if _, err := os.Stat(srcPath); err != nil && os.IsNotExist(err) {
			continue
		}
		data, err := ioutil.ReadFile(srcPath)
		if err != nil {
			logrus.Errorf("LSMD ERROR ioutil.ReadFile(srcDir:%s, name:%s) dstDir:%s, err:%s", srcDir, name, dstDir, err)
			return err
		}
		if err := ioutil.WriteFile(path.Join(dstDir, name), data, 0666); err != nil {
			logrus.Errorf("LSMD ERROR ioutil.WriteFile(path.Join(dstDir:%s, name:%s) srcDir:%s err:%s", dstDir, name, srcDir, err)
			return err
		}
	}
	return nil
}
