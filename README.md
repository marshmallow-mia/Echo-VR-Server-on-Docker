# Echo-Server-on-Docker

With this Dockerfiles and Scripts you can run the Echo Server Instances in Docker Containers.

As the x86 Version is made by modifing the Dockerfile from the ARM Version of Unusual Norm, a special thanks to him!

In the near future I will add an Error Check and autorestart of the Server Instances when errors happen.

If you want to pull the dockercontainer use:
```
docker pull ghcr.io/bl00dy-c0d3/echo-vr-server-on-docker:main
```
To start the container, you NEED to be inside the directory where your "ready-at-dawn-echo-arena"-Folder is located.
You can start the Container with:
```
docker run -d --rm -v $(pwd)/ready-at-dawn-echo-arena:/ready-at-dawn-echo-arena --network host ghcr.io/bl00dy-c0d3/echo-vr-server-on-docker:main -noovr -server -headless -timestep 120 -fixedtimestep -nosymbollookup -serverregion REGION
```
you need to change the REGION to one of these:
```
"uscn", // US Central North (Chicago)
"us-central-2", // US Central South (Texas)
"us-central-3", // US Central South (Texas)
"use", // US East (Virgina)
"usw", // US West (California)
"euw", // EU West 
"jp", // Japan (idk)
"sin", // Singapore oce region
```

If you want to allow the server instances to run on not more then X threads, you can use:
```
-numtaskthreads X
```
Dont set this to low, as it will need enough threads to run. 2 should be the minimum.
