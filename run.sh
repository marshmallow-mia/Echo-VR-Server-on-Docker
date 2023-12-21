#!/bin/bash
#check which ports are configures to use and how many
startport=$(grep -E 'port' ./ready-at-dawn-echo-arena/sourcedb/rad15/json/r14/config/netconfig_dedicatedserver.json | sed -e "s/[^0-9]//g")
portAmount=$(grep -E 'retries' ./ready-at-dawn-echo-arena/sourcedb/rad15/json/r14/config/netconfig_dedicatedserver.json | sed -e "s/[^0-9]//g")
maxPort=$(($startport+$portAmount))


#Do not change or things will break!
logfolder="./ready-at-dawn-echo-arena/logs"

# function to check which port can be used
function check_port_to_use {
a=$startport
	#while the port is lower then the max possible port
	while [ $a -le $maxPort ]
	do
		#check if this port is already in use
		portcheck=$(lsof | grep -c "UDP \*:$a")
		#if not in use, use this port to start the docker container
		if [ $portcheck = 0 ]
		then
			docker run -d --restart unless-stopped -v $(pwd)/ready-at-dawn-echo-arena:/ready-at-dawn-echo-arena -v ./scripts:/scripts  -p $a:$a/udp ready-at-dawn-echo-arena &
			exit
		fi
		((a++))
		# if there is no port available, error out and set too stdout+wall
		if [ $a -gt $maxPort ]
		then
			echo "NO AVAILABLE FREE PORTS. Please set them up at './ready-at-dawn-echo-arena/sourcedb/rad15/json/r14/config/netconfig_dedicatedserver.json'"
			wall "NO AVAILABLE FREE PORTS. Please set them up at './ready-at-dawn-echo-arena/sourcedb/rad15/json/r14/config/netconfig_dedicatedserver.json'"
		fi
	done
}

#this functions moves every old logfolder into ./old
function moveOldLogs {
	#create the "old" directory 
    mkdir $logfolder/old 2> /dev/null
	readarray -t container_ids < <(docker ps --format '{{.ID}}')
	readarray -t folders < <(find $logfolder/ -mindepth 1 -maxdepth 1  -type d -not -path "*/.*" )

	for folder in "${folders[@]}"
	do
		folderNeeded=0
		folder=$( echo $folder |cut -d "/" -f 4 )
		for id in ${container_ids[@]}
		do
			#If this will be set to $true, this folder is in use by a container
			#echo $id
			if [ "$folder" == "$id" ] ||  [ "$folder" == "old" ]
			then
				#mv "./ready-at-dawn-echo-arena/logs/$folder" "./ready-at-dawn-echo-arena/logs/old"
				folderNeeded=1
			fi
		done
			if [ $folderNeeded -eq 0 ]
			then
				mv $logfolder/$folder $logfolder/old/$folder 2> /dev/null
			fi
	done

	
}

moveOldLogs
check_port_to_use
