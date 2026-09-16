#!/bin/bash
. /etc/os-release

#Add architecture
dpkg --add-architecture i386

#Download RepoKeys and add (apt-key was removed in Debian 13)
mkdir -pm755 /etc/apt/keyrings
wget -O /etc/apt/keyrings/winehq-archive.key https://dl.winehq.org/wine-builds/winehq.key

#Add repositories and update (apt-add-repository is not available in Debian 13)
wget -NP /etc/apt/sources.list.d/ https://dl.winehq.org/wine-builds/debian/dists/$VERSION_CODENAME/winehq-$VERSION_CODENAME.sources
sed -i 's/^Components: main$/Components: main contrib/' /etc/apt/sources.list.d/debian.sources
apt update

#Install Wine, winetricks and some needed packages
apt install -y --install-recommends wine wine32 wine64 libwine libwine:i386 fonts-wine
apt install -y winetricks
apt install -y winbind

#add winhttp to our wine environment
winetricks winhttp

#wait for wineserver to exit so the registry changes are written to disk
wineserver -w
