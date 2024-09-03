#!/bin/bash
set -e
. /etc/os-release

branch="devel"
# NOTE: 9.x is currently semi-broken with "could not exec wineserver": https://github.com/ptitSeb/box86/issues/154
version="8.21~$VERSION_CODENAME-1"

mkdir -pm755 /etc/apt/keyrings
wget -O /etc/apt/keyrings/winehq-archive.key https://dl.winehq.org/wine-builds/winehq.key

if [ -n "$version" ]; then version="=$version"; fi
wget -NP /etc/apt/sources.list.d/ https://dl.winehq.org/wine-builds/$ID/dists/$VERSION_CODENAME/winehq-$VERSION_CODENAME.sources

dpkg --add-architecture i386 && dpkg --add-architecture amd64
apt-get update && apt-get download wine-$branch-i386:i386$version wine-$branch-amd64:amd64$version wine-$branch:amd64$version

# wget "https://dl.winehq.org/wine-builds/$ID/dists/$VERSION_CODENAME/main/binary-i386/wine-${branch}-i386_${version}_i386.deb"
# wget "https://dl.winehq.org/wine-builds/$ID/dists/$VERSION_CODENAME/main/binary-amd64/wine-${branch}-amd64_${version}_amd64.deb"
# wget "https://dl.winehq.org/wine-builds/$ID/dists/$VERSION_CODENAME/main/binary-amd64/wine-${branch}_${version}_amd64.deb"

get_dependencies() {
    local deb_file="$1"
    local dependencies=$(dpkg -I "$deb_file" | grep -oP ' Depends:.*$')
    IFS=',' read -ra parts <<< "$dependencies"
    local result=()

    for item in "${parts[@]}"; do
        trimmed_item=$(echo "$item" | awk '{$1=$1};1')
        result+=("${trimmed_item%% *}")
    done

    echo "${result[@]:1}"
}

wine32_dependencies=($(get_dependencies wine-${branch}-i386_*_i386.deb))
wine64_dependencies=($(get_dependencies wine-${branch}-amd64_*_amd64.deb))
wine_dependencies=($(get_dependencies wine-${branch}_*_amd64.deb))

for i in "${!wine32_dependencies[@]}"; do wine32_dependencies[$i]="${wine32_dependencies[$i]}:armhf"; done
for i in "${!wine64_dependencies[@]}"; do wine64_dependencies[$i]="${wine64_dependencies[$i]}:arm64"; done
for i in "${!wine_dependencies[@]}"; do
    wine_dependencies[$i]="${wine_dependencies[$i]}:armhf"
    if [[ ${wine_dependencies[$i]} == *"wine"* ]]; then unset wine_dependencies[$i]; fi
done

apt-get update && apt-get install --no-install-recommends -y "${wine32_dependencies[@]}" "${wine64_dependencies[@]}" "${wine_dependencies[@]}"
dpkg-deb -x wine-${branch}-i386_*_i386.deb wine-installer
dpkg-deb -x wine-${branch}-amd64_*_amd64.deb wine-installer
dpkg-deb -x wine-${branch}_*_amd64.deb wine-installer
mv wine-installer/opt/wine* ~/wine
rm -rf wine-installer/ wine-${branch}-i386_*_i386.deb wine-${branch}-amd64_*_amd64.deb wine-${branch}_*_amd64.deb

echo "box86 ~/wine/bin/wine \$@" >> /usr/local/bin/wine
echo "box64 ~/wine/bin/wine64 \$@" >> /usr/local/bin/wine64
ln -s ~/wine/bin/wineboot /usr/local/bin/wineboot
ln -s ~/wine/bin/winecfg /usr/local/bin/winecfg
echo "box64 ~/wine/bin/wineserver \$@" >> /usr/local/bin/wineserver
chmod +x /usr/local/bin/wine /usr/local/bin/wine64 /usr/local/bin/wineboot /usr/local/bin/winecfg /usr/local/bin/wineserver

wget https://raw.githubusercontent.com/Winetricks/winetricks/master/src/winetricks
chmod +x winetricks
mv winetricks /usr/local/bin

#apt install -y xvfb x11-xserver-utils xterm

# Set DISPLAY environment variable for X server
#export DISPLAY=:99

# Start Xvfb
#Xvfb :99 -screen 0 1024x768x16 &

apt install -y --install-recommends wine wine32 wine64 libwine libwine fonts-wine
apt install -y winbind


#add winhttp to our wine environment
winetricks winhttp
#winetricks corefonts vcrun6 vcrun2008



apt-get autoremove -y && apt-get clean autoclean && rm -rf /tmp/* /var/tmp/* /var/lib/apt/lists
