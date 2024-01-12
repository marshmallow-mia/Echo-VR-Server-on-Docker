#!/bin/bash

#Add architecture
dpkg --add-architecture i386

#Download RepoKeys and add
wget -nc https://dl.winehq.org/wine-builds/winehq.key
apt-key add winehq.key

#Add repositories and update 
apt-add-repository -y https://dl.winehq.org/wine-builds/debian/
apt-add-repository -y "deb http://ftp.de.debian.org/debian bullseye main contrib"
apt update

#Install Wine, winetricks and some needed packages
apt install -y --install-recommends wine wine32 wine64 libwine libwine:i386 fonts-wine
apt install -y winetricks
apt install -y winbind

#add winhttp to our wine environment
winetricks winhttp
