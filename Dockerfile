FROM debian:bullseye-slim as build

ENV DEBIAN_FRONTEND="noninteractive"

#Install some needed packages
RUN apt-get update \
 && apt-get install -y wget software-properties-common gnupg2

WORKDIR /root

# Clean up APT-Caches
RUN apt-get -y autoremove \
 && apt-get clean autoclean \
 && rm -rf /tmp/* /var/tmp/* /var/lib/apt/lists

# Install wine and winetricks
COPY install-wine.sh /
RUN bash /install-wine.sh \
 && rm /install-wine.sh


# SET the Echo Folder
VOLUME /ready-at-dawn-echo-arena
WORKDIR /ready-at-dawn-echo-arena/bin/win10
RUN wine wineboot

#Copy the demoprofile.json to the needes places
COPY ./demoprofile.json "/root/.wine/drive_c/users/root/AppData/Local/rad/echovr/users/dmo/"
VOLUME /root/.wine/drive_c/users/root/AppData/Local/rad/
ARG src="./demoprofile.json"
ARG target="/root/.wine/drive_c/users/root/Local Settings/Application Data/rad/echovr/users/dmo/demoprofile.json"
COPY ${src} ${target}

#SET Debug-Level and ENTRYPOINT
ENV WINEDEBUG=-all
ENV TERM=xterm
ENTRYPOINT ["wine", "/ready-at-dawn-echo-arena/bin/win10/echovr.exe"]
