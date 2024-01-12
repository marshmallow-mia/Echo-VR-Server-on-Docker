# Echo-Server-on-Docker
Docker based Echo VR Server with Error-Checking and restarts if an error occours.

With this Repo you can automatically install and configure everything  you need to run the Echo Server Instances in Docker Containers.

As the Dockerfile is based on the ARM Version of Unusual Norm, a special thanks to him!

There are 2 methods you can install this. 
1. with the install.sh: This automatically does install everything needed, builds the container and configures everything needed to get the server running
2. Building and setting everything up by yourself.

I will get into both methods. The install.sh method is prefered

**I will not provide the Echo-Folder that contains the binarys! You need to get it by yourself.**
**You also NEED the latest dbgcore.dll and pnsradgameserver.dll from the EchoTools Repo. Otherwise it will not work!** 
https://github.com/EchoTools/EchoRelay

# First clone this repo
## Install git:

Debian/Ubuntu:
```
apt install git
```
CentOS/Fedora:
CentOS doesnt seem to work for now.
```
yum install git
```
Arch:
```
pacman -S git
```
## Clone the Repo:
Clone the Repo, good folders are /opt or /srv as an example.

Clone it with:
```
git clone https://github.com/BL00DY-C0D3/Echo-VR-Server-on-Docker.git
```
**You need to have the Echo-Folder called  "ready-at-dawn-echo-arena" inside the folder you just cloned called "Echo-VR-Server-on-Docker"**

cd into **Echo-VR-Server-on-Docker** with
```
cd ./Echo-VR-Server-on-Docker
```

# Install using the install.sh script

As already mentioned this method does almost everything for you.
Just run the install.sh with 
```
bash ./install.sh
```
and it will ask you everything needed like IP, Port, Name, Region and more.

**You are done.**

To start a new container just run the run.sh script with 
```
bash ./run.sh
```
Every running Container will stay on as long as you dont stop them manually. Even after reboot!
See  at **Important Things** for more important informations.


# Install and configure it all by yourself

1. Install Docker
2. Install lsof (Not needed if you dont use the run.sh to start a container)
3. Build the container with:
  
   ``` 
   docker build -t ready-at-dawn-echo-arena . 
   ```
4. Configure the config.json at ./ready-at-dawn-echo-arena/_local/config.json
5. Configure the $region variable at ./scripts/start-echo.sh

# Start a container:

**1.** To start a container you can either just use the run.sh script by:

```
bash ./run.sh
```
This will check for an unsused port and automaticaly uses the lowest available port that is configured in your 
```
./ready-at-dawn-echo-arena/sourcedb/rad15/json/r14/config/netconfig_dedicatedserver.json
```
It also cleans up the log-folder by moving them into ./ready-at-dawn-echo-arena/logs/old

**2.**
Or you start it with the following command:
```
docker run -d --restart unless-stopped -v $(pwd)/ready-at-dawn-echo-arena:/ready-at-dawn-echo-arena -v ./scripts:/scripts  -p <inner_port>:<outer_port>/udp ready-at-dawn-echo-arena &
````

**It is important that you dont use the -host flag for remove of the network isolation between host and container, as this also changes the hostname of the container.
This will break it!**

**Every running Container will stay on as long as you dont stop them manually. Even after reboot!**


# Important Things:
- Logs are located in:
```
./ready-at-dawn-echo-arena/logs/<container_id>
```
- Show running containers:
```
docker ps
```
- Stop a running container:
```
docker stop <container_id>
```
- Stop all containers (Ignore the warnings):
```
docker stop $(docker ps)
```

- Change the start parameters of the Echo Server:
They are configured in the following file
```
./scripts/start-echo.sh
```

- If you want to allow the server instances to run on not more then X threads, you can use the following parameter.
Dont set this to low, as it will need enough threads to run. 2 should be the minimum.
```
-numtaskthreads X
```


- Every possible Flag you could set:

```
* -help: takes 0 arguments.
Displays cmd line help information.

* -crash: takes 0 arguments.
Force .exe to crash

* -crashdeferred: takes 0 arguments.
Force .exe to crash, but defer it later to the first update loop

* -level: takes 1 argument.
Possible params:
*  -level_name
Load a level to run

* -startlevel: takes 1 argument.
Possible params:
*  -level_name
Load a level to run

* -checkpoint: takes 1 argument.
Possible params:
*  -checkpoint_name
Active checkpoint to load into

* -startcheckpoint: takes 1 argument.
Possible params:
*  -checkpoint_name
Active checkpoint to load into

* -startmissioneditorcheckpoint: takes 0 arguments.
Use checkpoint selected in Mission Script Editor

* -nolevelloads: takes 0 arguments.
Prohibit level loads

* -noaudio: takes 0 arguments.
Disable audio

* -speakersetup: takes 1 argument.
Possible params:
*  -speaker_setup
Speaker setup (stereo/5point1)

* -panningrule: takes 1 argument.
Possible params:
*  -panning_rule
Panning rule (speakers/headphones)

* -defaultports: takes 0 arguments.
Turns off dynamic ports for Wwise

* -dataroot: takes 1 argument.
Possible params:
*  -data_root
Set an alternative to data dir root (defaults to the project dir)

* -datadir: takes 1 argument.
Possible params:
*  -data_dir
Set an alternative to _data to load data from

* -nosymbollookup: takes 0 arguments.
Do not use symbol lookup

* -package: takes 1 argument.
Possible params:
*  -package_name
Package name to use for resource loading

* -reportcrashes: takes 0 arguments.
Report crashes

* -legacyvis: takes 0 arguments.
Use the legacy (precomputed) visibility system

* -capturevideo: takes 0 arguments.
Enable video capture, must also use -capture360

* -captureaudio: takes 0 arguments.
Enable audio capture

* -fullscreen: takes 0 arguments.
Starts the game in fullscreen mode

* -display: takes 1 argument.
Specifies the index of the display used for fullscreen mode

* -adapter: takes 1 argument.
Specifies the index of the GPU to use

* -fullscreen_res: takes 2 arguments.
Sets the target fullscreen resolution

* -language: takes 1 argument.
Command to switch the start language

* -headless: takes 0 arguments.
Run the game with no graphics

* -fixedtimestep: takes 0 arguments.
Tells the game to run at a fixed time step

* -syncinterval: takes 1 argument.
Specifies the VSYNC interval to use

* -nomsaa: takes 0 arguments.
Disable MSAA

* -vrscale: takes 1 argument.
Possible params:
*  -vr_scale
Scale VR resolution to improve performance

* -smoothrotation: takes 0 arguments.
Enable smooth rotation

* -smoothroll: takes 0 arguments.
Enable smooth roll

* -teststart: takes 0 arguments.
Start game then stop to test for memleaks

* -msaa: takes 1 argument.
Possible params:
*  -msaa_mode
The MSAA mode to use

* -usetouch: takes 0 arguments.
Use touch controls for the game

* -mp: takes 0 arguments.
Boot into multiplayer

* -logpath: takes 1 argument.
Path for the log file to go to

* -uniquelogdir: takes 0 arguments.
Write all log files to a unique directory

* -usercfgpath: takes 1 argument.
Path where the local config is located

* -usevrsize: takes 0 arguments.
Size the window to VR native resolution

* -capture: takes 0 arguments.
Run the game in a mode designed for recording with a capture device

* -capturevp2: takes 0 arguments.
Run the game in recording mode, using a second viewport.

* -dumpstats: takes 0 arguments.
Dump perfstats after 60 sec and exit for automated perf collection

* -defaultsettings: takes 0 arguments.
Force the game to auto-pick graphics settings

* -lobbyid: takes 1 argument.
ID of lobby to join on start

* -lobbyteam: takes 1 argument.
Team to join on start

* -moderator: takes 0 arguments.
Join the logged in user's lobby group as a moderator

* -moderateuser: takes 1 argument.
Join the given user's lobby as a moderator

* -moderategroup: takes 1 argument.
Join the given lobby group as a moderator

* -displayname: takes 1 argument.
Set the logged in user's display name (if allowed)

* -micprovider: takes 1 argument.
Set the desired mic provider (OVR, RAD, DMO)

* -port: takes between 1 and 2 arguments.
First port to try binding to (dedicated server only)

* -mpappid: takes 1 argument.
Override multiplayer app id

* -spectatorstream: takes between 0 and 1 arguments.
Stream spectator mode matches

* -numtaskthreads: takes 1 argument.
Change the number of task threads that startup

* -httpport: takes 1 argument.
Port for HTTP listener to use

* -noovr: takes 0 arguments.
Disable OVR platform features

* -region: takes 1 argument.
Which region to use when searching for a server

* -gametype: takes 1 argument.
Game type to create / find / join on start

* -publisherlock: takes 1 argument.
Set the publisher lock

* -confighost: takes 1 argument.
Set the config host endpoint

* -loginhost: takes 1 argument.
Set the login host endpoint

* -radserverdbhost: takes 1 argument.
Set the sdb host endpoint

* -servermanagerhost: takes 1 argument.
Set the sdb host endpoint

* -servercountry: takes 1 argument.
Set server country

* -serverlocation: takes 1 argument.
Set server location

* -serverplugin: takes 1 argument.
Set server plugin

* -serverregion: takes 1 argument.
Set server region

* -server: takes 0 arguments.
[EchoRelay] Run as a dedicated game server

* -offline: takes 0 arguments.
[EchoRelay] Run the game in offline mode

* -windowed: takes 0 arguments.
[EchoRelay] Run the game with no headset, in a window

* -timestep: takes 1 argument.
[EchoRelay] Sets the fixed update interval when using -headless (in ticks/updates per second). 0 = no fixed time step, 120 = default
```
