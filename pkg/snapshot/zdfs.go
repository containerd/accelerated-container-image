package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/storage"
	dockermount "github.com/docker/docker/pkg/mount"
	"github.com/moby/sys/mountinfo"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	writeType   int
	zdfsIsReady bool //indicate if zdfs' binaries or rpms are ready
)

const (
	deviceName = "dev_name"

	dataFile            = ".data_file"             //top layer data file for lsmd
	idxFile             = ".data_index"            //top layer index file for lsmd
	zdfsMerged          = "zdfsmerged"             //merged dir that containes all the context of all dadi lower dirs
	zdfsMetaDir         = "zdfsmeta"               //meta dir that contains the dadi image meta files
	iNewFormat          = ".aaaaaaaaaaaaaaaa.lsmt" //characteristic file of dadi image
	zdfsChecksumFile    = ".checksum_file"         //file containing the checksum data if each dadi layer file to guarantee data consistent
	zdfsOssurlFile      = ".oss_url"               //file containing the address of layer file
	zdfsOssDataSizeFile = ".data_size"             //file containing the size of layer file
	zdfsOssTypeFile     = ".type"                  //file containing the type, such as layern, commit(layer file on local dir), oss(layer file is in oss
	zdfsOssTypeTopLayer = "layern"                 //type that indicates that this layer is the top layer for lsmd
	zdfsTrace           = ".trace"

	overlaybdBaseLayerDir = "/opt/overlaybd/baselayers"
	overlaybdBaseLayer    = "/opt/overlaybd/baselayers/.commit"

	lsmtCreate = "/opt/lsmd/bin/lsmt_create" //binary path that is used to creteat top layer files for lsmd

	ImageRefFile = "image_ref" // save cri.imageRef as file for old dadi format

	SandBoxMetaFile = "pod_sandbox_meta" // for SAE
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
	exists, _ := pathExists(path.Join(dir, "zdfsmeta", "config.v1.json"))
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

func scsiBlockDevicePath(deviceNumber string) string {
	return fmt.Sprintf("/sys/class/scsi_device/%s:0/device/block", deviceNumber)
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

func getZdfsmerged(dir string) string {
	return path.Join(dir, zdfsMerged)
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

//check necessary binaries and dirs
func CheckLsmdNecessity(engine string) bool {
	if zdfsIsReady {
		return true
	}

	if engine == "tcmu" {
		paths := []string{overlaybdBaseLayer}
		for _, p := range paths {
			b, err := pathExists(p)
			if err != nil {
				logrus.Errorf(" %s doesn't exist. err:%s", p, err)
				return false
			}
			if b == false {
				logrus.Errorf(" %s doesn't exist.", p)
				return false
			}
		}
		// use service ExecStartPre to do modprobe
		// if !CheckAndModprobe("target_core_user") {
		// 	logrus.Errorf("failed to modprobe target_core_user(tcmu) module, try use vrbd")
		// 	return false
		// }
		zdfsIsReady = true
		return true
	}

	return false
}

func GetShared(snDir string, lowerDirs []string, upper string, engine string) (devName string, retErr error) {
	devName = ""
	if len(lowerDirs) <= 0 {
		return devName, fmt.Errorf("LSMD GetShared ERROR empty lowerDirs")
	}

	logrus.Infof("LSMD Enter GetShared, image top layer: %s, upper: %s", lowerDirs[0], upper)
	defer logrus.Infof("LSMD Exit GetShared, image top layer: %s, upper: %s", lowerDirs[0], upper)

	topLayerDir := lowerDirs[0]
	if upper != "" {
		topLayerDir = upper
	}
	if b, err := pathExists(topLayerDir); !b {
		logrus.Errorf("LSMD GetShared ERROR topdir:%s doesn't exist. err:%s", topLayerDir, err)
		return devName, fmt.Errorf("LSMD GetShared ERROR topdir:%s doesn't exist. err:%s", topLayerDir, err)
	}

	// 直接挂在toplayer的zdfsmerged，不存在则创建
	dst := getZdfsmerged(topLayerDir)
	logrus.Infof("toplayer: %s", dst)
	b, _ := pathExists(dst)
	if !b {
		if err := os.MkdirAll(dst, 0755); err != nil && !os.IsExist(err) {
			logrus.Errorf("LSMD ERROR os.MkdirAs(%s, 0755) err:%s", dst, err)
			return devName, fmt.Errorf("LSMD GetShared ERROR os.MkdirAs(%s, 0755). err:%s", dst, err)
		}
	}
	// overwrite sandbox meta to image's top layer.
	sandBoxMetaSrc := path.Join(snDir, SandBoxMetaFile)
	if _, err := os.Stat(sandBoxMetaSrc); err == nil {
		sandBoxMetaCopy := path.Join(lowerDirs[0], SandBoxMetaFile)
		logrus.Infof("overwrite sandbox meta into %s", sandBoxMetaCopy)
		input, _ := ioutil.ReadFile(sandBoxMetaSrc)
		if err := ioutil.WriteFile(sandBoxMetaCopy, input, 0644); err != nil {
			logrus.Errorf("overwrite sandbox meta into %s failed: %v", sandBoxMetaCopy, err)
			return devName, err
		}
	}
	b, err := mountinfo.Mounted(dst)
	if err != nil {
		logrus.Errorf("LSMD ERROR can't get mount status of %s", dst)
		return devName, err
	}

	if b {
		logrus.Warnf("LSMD WARN %s has been already mounted.", dst)
		return devName, nil
	}

	if engine == "tcmu" {
		l := strings.LastIndex(topLayerDir, "/")
		if devName, err = tcmuMount(topLayerDir[l+1:], topLayerDir, dst, lowerDirs, "", upper != ""); err != nil {
			logrus.Errorf("LSMD ERROR tcmuMount %s fail, error: %v", topLayerDir, err)
			return devName, err
		}
	} else {
		return "", fmt.Errorf("bad engine: %s", engine)
	}
	return devName, nil
}

func Get(snDir string, lowers []string, upperdir string, engine string) (mergedDir string, retErr error) {
	logrus.Infof("LSMD enter Get(%s)", lowers)
	defer logrus.Infof("LSMD leave Get(%s), merged Dir: %s", lowers, mergedDir)

	if CheckLsmdNecessity(engine) == false {
		logrus.Errorf("LSMD ERROR failed to checkLsmdNecessity().")
		return "", fmt.Errorf("LSMD ERROR failed to checkLsmdNecessity().")
	}

	if len(lowers) <= 0 {
		logrus.Errorf("LSMD ERROR len(lowers):%d should be >= 1", len(lowers))
		return "", fmt.Errorf("LSMD ERROR len(lowers) should be >= 1")
	}

	//launch new lsmd
	if mergedDir, retErr = GetShared(snDir, lowers, upperdir, engine); retErr != nil {
		return "", retErr
	}
	if upperdir == "" {
		return getZdfsmerged(lowers[0]), nil
	}
	return mergedDir, nil
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
	log.G(ctx).Debugf("dir: %s", dir)
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
	//getMergedDir
	engine := "tcmu"
	zdfsMergedDir, err := Get(snDir, lowers, "", engine)
	if err != nil {
		return true, "", err
	}
	dst := path.Join(snDir, "omerged")
	if err := os.MkdirAll(dst, 0755); err != nil && !os.IsExist(err) {
		logrus.Errorf("LSMD Mounts() ERROR os.MkdirAs(%s, 0755) err:%s", dst, err)
		return true, "", fmt.Errorf("LSMD Mounts() ERROR os.MkdirAs(%s, 0755). err:%s", dst, err)
	}

	m, err := dockermount.Mounted(dst)
	if err != nil {
		logrus.Errorf("LSMD ERROR devId:%d, mount.Mounted(%s) err:%s", dst, err)
		return true, "", err
	}
	if !m {
		var options []string
		options = append(options,
			fmt.Sprintf("workdir=%s", o.workPath(s.ID)),
			fmt.Sprintf("upperdir=%s", o.upperPath(s.ID)),
		)
		options = append(options, fmt.Sprintf("lowerdir=%s", zdfsMergedDir))
		if o.metacopyOption != "" {
			options = append(options, o.metacopyOption)
		}
		logrus.Infof("[DADI] mount options : %s", options)

		if err := mount.All([]mount.Mount{
			{
				Type:    "overlay",
				Source:  "overlay",
				Options: options,
			},
		}, dst); err != nil {
			return true, "", err
		}
	}

	return true, dst, nil
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
		Lowers:     []OverlayBDBSConfigLower{},
		ResultFile: overlaybdInitDebuglogPath(dir),
	}
	configJSON.RepoBlobURL = repo
	if parent == "" {
		configJSON.Lowers = append(configJSON.Lowers, OverlayBDBSConfigLower{
			Dir: overlaybdBaseLayerDir,
		})
	} else {
		parentConfJSON, err := loadBackingStoreConfig(parent)
		if err != nil {
			return err
		}
		if repo == "" {
			configJSON.RepoBlobURL = parentConfJSON.RepoBlobURL
		}
		// configJSON.RepoBlobURL = parentConfJSON.RepoBlobURL
		configJSON.Lowers = parentConfJSON.Lowers
	}

	configJSON.RecordTracePath = recordTracePath
	// if configJSON.RepoBlobURL == "" {

	// }
	refPath := path.Join(dir, ImageRefFile)
	logrus.Debugf("! refPath: %s", refPath)
	if b, _ := pathExists(refPath); b {
		img, _ := ioutil.ReadFile(refPath)
		configJSON.ImageRef = string(img)
		logrus.Infof("read imageRef from %s: %s", refPath, configJSON.ImageRef)
	}

	configJSON.Lowers = append(configJSON.Lowers, OverlayBDBSConfigLower{
		Digest: digest,
		Size:   int64(size),
		Dir:    path.Join(dir, zdfsMetaDir),
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
		logrus.Info("ENTER makeConfig(dir: %s, parent: %s)", dir, parent)
		dstDir := path.Join(dir, zdfsMetaDir)
		repo, digest, err := GetBlobRepoDigest(dstDir)
		if err != nil {
			return err
		}
		refPath := path.Join(dir, ImageRefFile)
		if b, _ := pathExists(refPath); b {
			img, _ := ioutil.ReadFile(refPath)
			imageRef := string(img)
			logrus.Infof("read imageRef from %s: %s", refPath, imageRef)
			repo, _ = constructImageBlobURL(imageRef)
			logrus.Infof("construct repoBlobUrl: %s", repo)
		}
		size, err := GetBlobSize(dstDir)
		if err := ConstructOverlayBDSpec(dir, parent, repo, digest, info, size, recordTracePath); err != nil {
			return err
		}
		logrus.Info("makeConfig success")
		return nil
	}

	doDir := func(dir string, parent string) error {
		logrus.Debugf("ENTER doDir(dir: %s)", dir)
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

		//2.create tmpDir in dir
		tmpDir, err := ioutil.TempDir(dir, "temp_for_prepare_dadimeta")
		if err != nil {
			logrus.Errorf("LSMD ERROR ioutil.TempDir(%s,.) err:%s", dir, err)
			return err
		}

		//3.copy meta files to tmpDir
		srcDir := path.Join(dir, "fs")
		if err := copyPulledZdfsMetaFiles(srcDir, tmpDir); err != nil {
			logrus.Errorf("failed to copyPulledZdfsMetaFiles(%s, %s), err:%s", srcDir, tmpDir, err)
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

func createRWlayer(dir, parent, engine string) error {

	rwDir := path.Join(dir, zdfsMetaDir)
	if err := os.MkdirAll(rwDir, 0755); err != nil && !os.IsExist(err) {
		logrus.Errorf("create rw layer's dir failed: %s, err %s", rwDir, err.Error())
		return err
	}
	//only write on blank
	if err := ioutil.WriteFile(path.Join(rwDir, iNewFormat), []byte(" "), 0666); err != nil {
		logrus.Errorf("LSMD ERROR ioutil.WriteFile(path.Join(dir:%s, iNewFormat:%s),..) err:%s", dir, iNewFormat, err)
		return err
	}
	//writing “layern” indicates this is zdfs top dir
	if err := ioutil.WriteFile(path.Join(rwDir, zdfsOssTypeFile), []byte(zdfsOssTypeTopLayer), 0666); err != nil {
		logrus.Errorf("LSMD ERROR ioutil.WriteFile(path.Join(dir:%s, zdfsOssTypeFile:%s), zdfsOssTypeTopLayer:%s), err:%s", dir, zdfsOssTypeFile, zdfsOssTypeTopLayer, err)
		return err
	}
	str := lsmtCreate
	if engine == "tcmu" {
		str = overlaybdCreate
	}
	str = str + " -s " + path.Join(rwDir, dataFile)
	str = str + " " + path.Join(rwDir, idxFile)
	str = str + " 256 "
	if err := execCmd(str); err != nil {
		return err
	}

	return constructOverlayBDWritableSpec(dir, parent)
}
