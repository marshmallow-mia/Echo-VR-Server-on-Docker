#!/bin/bash
set -e

wget https://itai-nelken.github.io/weekly-box86-debs/debian/box86.list -O /etc/apt/sources.list.d/box86.list
wget -qO- https://itai-nelken.github.io/weekly-box86-debs/debian/KEY.gpg | gpg --dearmor -o /etc/apt/trusted.gpg.d/box86-debs-archive-keyring.gpg

wget https://ryanfortner.github.io/box64-debs/box64.list -O /etc/apt/sources.list.d/box64.list
wget -qO- https://ryanfortner.github.io/box64-debs/KEY.gpg | gpg --dearmor -o /etc/apt/trusted.gpg.d/box64-debs-archive-keyring.gpg

dpkg --add-architecture armhf && dpkg --add-architecture arm64
apt-get update && apt-get install --no-install-recommends -y box86:armhf box64:arm64

apt-get autoremove -y && apt-get clean autoclean && rm -rf /tmp/* /var/tmp/* /var/lib/apt/lists
