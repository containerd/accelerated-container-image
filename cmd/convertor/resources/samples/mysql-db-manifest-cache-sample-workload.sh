# Validation Examples
registry=$1 # registry to push to
username=$2 # username for registry
password=$3 # password for registry
sourceImage=$4 # public image to convert
repository=$5 # repository to push to
tag=$6 # tag to push to
mysqldbuser=$7 # mysql user
mysqldbpassword=$8 # mysql password

oras login $registry -u $username -p $password
oras cp $sourceImage $registry/$repository:$tag
# Try one conversion
./bin/convertor --repository $registry/$repository -u $username:$password --input-tag $tag --oci --overlaybd $tag-obd-cache --db-str "$mysqldbuser:$mysqldbpassword@tcp(127.0.0.1:3306)/conversioncache" --db-type mysql

# Retry, result manifest should be cached
./bin/convertor --repository $registry/$repository -u $username:$password --input-tag $tag --oci --overlaybd $tag-obd-cache-2 --db-str "$mysqldbuser:$mysqldbpassword@tcp(127.0.0.1:3306)/conversioncache" --db-type mysql

# Retry, cross repo mount
oras cp $sourceImage $registry/$repository-2:$tag
./bin/convertor --repository $registry/$repository-2 -u $username:$password --input-tag $tag --oci --overlaybd $tag-obd-cache-3 --db-str "$mysqldbuser:$mysqldbpassword@tcp(127.0.0.1:3306)/conversioncache" --db-type mysql

# Expected output in the registry:
# <Repository>
#    -- <Tag>
#    -- <Tag>-obd-cache
#    -- <Tag>-obd-cache-2
# <Repository>-2
#    -- <Tag>
#    -- <Tag>-obd-cache-3
