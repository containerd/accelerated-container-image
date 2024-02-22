#!/bin/sh
# Reference Script for getting started with mysql DB for userspace convertor
mysqldbuser=$1
mysqldbpassword=$2

# Set up mysql
apt update
apt install mysql-server
service mysql start
cat ./mysql.conf | mysql
echo "CREATE USER '$mysqldbuser'@'localhost' IDENTIFIED BY '$mysqldbpassword'; GRANT ALL PRIVILEGES ON conversioncache.* TO '$mysqldbuser'@'localhost'; FLUSH PRIVILEGES;" | mysql