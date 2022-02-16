# Image Convertor

We provide a ctr command tool to convert OCIv1 images into overlaybd format, which is stored in `bin` after `make` or downloading the release package.

# Basic Usage

```bash
# pull the source image
sudo ctr i pull registry.hub.docker.com/library/redis:6.2.1

# call our ctr to do image conversion
# bin/ctr obdconv <src-image> <dst-image>
sudo bin/ctr obdconv registry.hub.docker.com/library/redis:6.2.1 registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new

# push
ctr i push registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new
```

# Layer Deduplication

To avoid converting the same layer for every image conversion, a database is required to store the correspondence between OCIv1 image layer and overlaybd layer.

We provide an implementation based on mysql database.

First, create a database and the `overlaybd_layers` table, the table schema is as follow:

```sql
CREATE TABLE `overlaybd_layers` (
  `host` varchar(255) NOT NULL,
  `repo` varchar(255) NOT NULL,
  `chain_id` varchar(255) NOT NULL COMMENT 'chain-id of the normal image layer',
  `data_digest` varchar(255) NOT NULL COMMENT 'digest of overlaybd layer',
  `data_size` bigint(20) NOT NULL COMMENT 'size of overlaybd layer',
  PRIMARY KEY (`host`,`repo`,`chain_id`),
  KEY `index_registry_chainId` (`host`,`chain_id`) USING BTREE
) DEFAULT CHARSET=utf8;
```

Then, execute the ctr obdconv tool:

```bash
# pull the source image
sudo ctr i pull registry.hub.docker.com/library/redis:6.2.1

# call our ctr to do image conversion
# bin/ctr obdconv --dbstr dbstr -u registry_username:password <src-image> <dst-image>
sudo bin/ctr obdconv --dbstr "username:password@tcp(db_host:port)/db_name" registry.hub.docker.com/library/redis:6.2.1 registry.hub.docker.com/overlaybd/redis:6.2.1_obd_new
```

After that, the new overlaybd image automatically is uploaded to registry and the layers correspondences are saved to database.

The `dbstr` is the config string of database, please refer to [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql).
The other options are the same as `ctr content push-object`. For the converted blobs have to be pushed to registry during conversion to synchronize registry with database, the registry related options must be provided. The most important is the `--user` option which is used for authentication.
