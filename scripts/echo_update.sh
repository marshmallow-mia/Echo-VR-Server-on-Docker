#!/bin/bash
cd $(readlink -f $(dirname $0))

rsyncCommand="rsync -crtz --exclude-from="../files/exclude.list" --progress --partial --compress-level=0 "
rsyncUpdateCheckCommand="rsync -cnrv --exclude-from="../files/exclude.list" "
#Path of the echo folder
echoPath="../ready-at-dawn-echo-arena"



function updateEcho {
    if ! [[ -f ../rsyncHosts ]]
    then
        echo "The rsyncHosts-file is missing. The script will be stopped now. It needs to be located at $PWD"
        echo -e '\033[0m' # No Color
        exit
    fi
    readarray -t rsyncHosts < ../rsyncHosts
    serverCounter=0
    
    for host in ${rsyncHosts[@]}
    do        
        echo "Testing Server $( echo "$serverCounter+1" | bc)"
        #CHECK DOWNLOAD SPEED
        rm ./temp/malgun.ttf 2> /dev/null
        downloadStart[$serverCounter]=`date +%s.%N`
        output[$serverCounter]=$( $rsyncCommand$host/content/engine/core/fonts/malgun.ttf ./temp/ | tee /dev/tty )
        rm -r ./temp
        downloadEnd[$serverCounter]=`date +%s.%N`
        #If there was an error. set the time to 99999, otherwise calculate the real time
        if [[ "${output[$serverCounter]}" =~ 100\%.* ]]
        then
            speedtest[$serverCounter]=$( echo "${downloadEnd[$serverCounter]}-${downloadStart[$serverCounter]}" | bc )
        else
            speedtest[$serverCounter]=99999
        fi
        ((serverCounter++))
    done
    
    checkCounter=0
    fastestServer=0
    #check which server was the fastest
    while [[ $checkCounter+1 -lt ${#rsyncHosts[@]} ]]
    do
        calculate=$( echo "${speedtest[fastestServer]}-${speedtest[$checkCounter+1]}" | bc)
        #If calculate is not -XXX, the $checkCounter+1 is faster
        if ! [[ $calculate =~ -.* ]]
        then
            fastestServer=$(echo "$checkCounter+1" | bc)
        fi
        ((checkCounter++))
    done
    
    #If the winning time is 99999 there is an error on all server, exit the script
    if [[ "${speedtest[$fastestServer]}" =~ 99999 ]]
    then
        echo -e '\033[0;31m' #write in red
        echo "No connection to any Download-Server possible. Please try again."
        wall "No Echo-Updateserver reachable"
        echo -e '\033[0m' # No Color
        exit
    fi
            
    echo "Download starts with host $fastestServer, speed: ${speedtest[$fastestServer]}"
    #Start the download
    $rsyncCommand${rsyncHosts[$fastestServer]}/. ../ready-at-dawn-echo-arena       
    #start the servers back up
    source ./slowlyCloseServers.sh --start
}


#This function checks if an update is available
function checkForUpdates {
    #Check if the rsyncHosts-file exists
    if ! [[ -f ../rsyncHosts ]]
    then
        echo -e '\033[0;31m' #write in red
        wall "The rsyncHosts-file is missing. The script will be stopped now. It needs to be located at $PWD"
        echo -e '\033[0m' # No Color
        exit
    fi
    echo -e '\033[0;31m' #write in red
    echo "Checking for updates now"
    echo -e '\033[0m' # No Color
    #Check for updated files, exclude files in ../files/exclude.list
    readarray -t rsyncHosts < ../rsyncHosts
    
    counter=0
    while ! [[ $rsyncCheck ]]
    do
        if [[ $counter -eq ${#rsyncHosts[@]} ]]
        then
            wall "No Echo-Updateserver reachable. Exit"
            exit
        fi
        
        rsyncCheck=$($rsyncUpdateCheckCommand${rsyncHosts[$counter]} $echoPath/  2>&1 \
               | sed -e "/receiving/d" -e "/received.*sec/d" -e "/total size/d" -e "/^$/d" )
        #If not set, no update
        if ! [[ $rsyncCheck ]]
        then
            echo "no Update found"
            exit
        fi
        #if set but containes error, continue to next server, by unsetting the var
        if [[ "$rsyncCheck" =~ "rsync error" ]]
        then
            unset rsyncCheck
        fi
        echo $rsyncCheck
        ((counter++))
    done

    echo "Updates found"
        source ./slowlyCloseServers.sh
    #start the Update
    updateEcho
}

if [ "$(pgrep -f echo_update.sh | wc -l)" -gt 3 ]  || [ "$(pgrep -f install.sh | wc -l)" -gt 0 ]
then
    exit
fi
checkForUpdates
