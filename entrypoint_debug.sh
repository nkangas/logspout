#!/bin/sh

# Test connection to the AWS EC2 metadata API with timeout of 1 second
nc -vz -w 1 169.254.169.254 80

# If the connection succeeded (exit code == 0)  then use it to populate the hostname override file for Logspout.
if [ "$?" -eq "0"  ]; then
  echo "Setting Hostname via AWS EC2 Metadata API..."
  curl http://169.254.169.254/latest/meta-data/instance-id > /etc/host_hostname
else
  nc -vz -w 1 rancher-metadata 80

  if [ "$?" -eq "0"  ]; then
    echo "Setting Hostname via Rancher Metadata API..."
    curl http://rancher-metadata/2015-12-19/self/host/hostname > /etc/host_hostname
  fi
fi
