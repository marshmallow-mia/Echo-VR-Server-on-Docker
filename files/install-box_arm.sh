#!/bin/bash
set -e

wget https://itai-nelken.github.io/weekly-box86-debs/debian/box86.list -O /etc/apt/sources.list.d/box86.list
wget -qO- https://itai-nelken.github.io/weekly-box86-debs/debian/KEY.gpg | gpg --dearmor -o /etc/apt/trusted.gpg.d/box86-debs-archive-keyring.gpg

#wget https://ryanfortner.github.io/box64-debs/box64.list -O /etc/apt/sources.list.d/box64.list
#wget -qO- https://ryanfortner.github.io/box64-debs/KEY.gpg | gpg --dearmor -o /etc/apt/trusted.gpg.d/box64-debs-archive-keyring.gpg

dpkg --add-architecture armhf && dpkg --add-architecture arm64
apt-get update && apt-get install --no-install-recommends -y box86:armhf

wget -O /tmp/box64.deb https://raw.githubusercontent.com/BL00DY-C0D3/box64-debs/refs/heads/master/debian/box64_0.3.3%2B20250109.d55e879-1_arm64.deb



wget -O /tmp/box64.deb https://raw.githubusercontent.com/BL00DY-C0D3/box64-debs/refs/heads/master/debian/box64_0.3.3+20250109.6cdfa05-1_arm64.deb

apt install -y /tmp/box64.deb

apt-get autoremove -y && apt-get clean autoclean && rm -rf /tmp/* /var/tmp/* /var/lib/apt/lists

rm -f /tmp/box64.deb
