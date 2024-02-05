#!/bin/bash
shutdownMessages=('[ECHORELAY.GAMESERVER] Signaling end of session'
'[NSLOBBY] registration successful'
'[NETGAME] NetGame switching state (from logged in, to lobby)'
'[TCP CLIENT] [R14NETCLIENT] connection to'
'[LEVELLOAD] Unloading level')
#shutdownMessages=("bla d" "blabla" "blablabla")
#Path of the echo folder
echoPath="../ready-at-dawn-echo-arena"
#The name of the Containers
containerName=$( grep dockerContainerName= ../run.sh | sed -e "s/dockerContainerName=\"//g" -e "s/\"//g" )

function startServer {
    a=0
    while [[ $a -lt $amountRunningServer ]]
    do
        bash ../run.sh
        ((a++))
        sleep 5
    done    
}

function updateEcho {
    server1="rsync -crtz --exclude-from="../files/exclude.list" --progress --partial --compress-level=0 evr@echo.marceldomain.de::files"
    server2="rsync -crtz --exclude-from="../files/exclude.list" --progress --partial --compress-level=0 evr@nakama0.eu-west.echovrce.com::files"
#CHECK DOWNLOAD SPEED
    echo -e '\033[0;31m' #write in red
    echo "The update will now be downloaded. We will test the Download-Speeds now to automatically choose the fastest server."
    echo "Please do not abort! Depending on the connection to the server it could take like 2 min. The filesize is like 10mb btw."
    echo -e '\033[0m' # No Color
    rm ./temp/malgun.ttf 2> /dev/null
    
    #Check server 1
    local downloadStart1=`date +%s.%N`
    output1=$( $server1/content/engine/core/fonts/malgun.ttf ./temp/ | tee /dev/tty )
    rm -r ./temp
    local downloadEnd1=`date +%s.%N`
    local speedtest1=$( echo "$downloadEnd1-$downloadStart1" | bc )
    
    echo -e '\033[0;31m' #write in red
    echo "Next server will be tested now"
    echo -e '\033[0m' # No Color
    
    #Check server 2
    local downloadStart2=`date +%s.%N`S
    output2=$( $server2/content/engine/core/fonts/malgun.ttf ./temp/ | tee /dev/tty )
    rm -r ./temp
    local downloadEnd2=`date +%s.%N`        
    local speedtest2=$( echo "$downloadEnd1-$downloadStart1" | bc )
    
    choosenServer=0 #If this changes in the following "if", there is no need for a comparison
    #Check if the Downloads succeeded
    # if error in one of the rsyncs
    if ! [[ "$output1" =~ 100\%.* ]] || ! [[ "$output2" =~ 100\%.* ]] 
    then
        if ! [[ "$output1" =~ 100\%.* ]]
        then
            choosenServer=2
            if ! [[ "$output2" =~ 100\%.* ]]
            then
                echo "No connection to any Download-Server possible. Please try again."
                echo -e '\033[0m' # No Color
                exit
            fi
        else
            choosenServer=1
        fi
    fi
    

    if [[ $choosenServer == 0 ]]
    then
        speedTestResult=$( echo "$speedtest1 - $speedtest2" | bc )
        if [[ "$speedTestResult" =~ "-.*" ]]
        then
            choosenServer=2
        else
            choosenServer=1
        fi
    fi
    
    echo -e '\033[0;31m' #write in red
    echo "The update will begin now."
    echo -e '\033[0m' # No Color
    
    if [[ $choosenServer == 1 ]]
    then
        $server1/. $echoPath/
    else
        $server2/. $echoPath/
    fi
    #start the servers back up
    startServer
}

#This function slowly closes the server if no one is on it.
function slowlyCloseServers {
        #Save each Docker Container ID in an array
        mapfile -t runningContainer < <(docker ps --filter "ancestor=$containerName" --format "{{.ID}}")
        
        if ! [[ $secondRun ]]
        then
            #save the amount of running servers        
            declare -g amountRunningServer=${#runningContainer[@]}
            echo $amountRunningServer "Servers are running. We will slowly close them now."
            secondRun=1
        fi
        
        #check logs of each Container to close it.
        for id in ${runningContainer[*]}
        do
            toClose=0
            #get last 10 lines and check each one
            logContent=$(tail -5 $echoPath/logs/$id/*.log) 
            for message in "${shutdownMessages[@]}"
            do
                if echo "$logContent" | grep -q -F "$message"
                then
                    toClose=1
                fi
            done
            #if [[ $logContent =~ ${shutdownMessages[@]} ]]
            #then
            #    toClose=1
            #fi
            
            if [[ $toClose == 1 ]]
            then
                docker stop $id
            fi
            
        done
        
}



#This function checks if an update is available
function checkForUpdates {
    #Check for updated files, exclude files in ../files/exclude.list
    rsyncCheck=$(rsync -cnrv --exclude-from="../files/exclude.list" evr@echo.marceldomain.de::files/ $echoPath/  2>&1 \
               | sed -e "/receiving/d" -e "/received.*sec/d" -e "/total size/d" -e "/^$/d" )
    if [[ "$rsyncCheck" =~ "rsync error" ]]
    then
        rsyncCheck=$(rsync -cnrv --exclude-from="../files/exclude.list" evr@nakama0.eu-west.echovrce.com::files/ .$echoPath/  2>&1 \
                   | sed -e "/receiving/d" -e "/received.*sec/d" -e "/total size/d" -e "/^$/d" )
    fi
    #If both server arent reachable, send an error to wall and exit
    if [[ "$rsyncCheck" =~ "rsync error" ]]
    then
        wall "Error while checking for Echo Server Updates. Unable to contact any server"
        exit
    fi
    
    if [[ $rsyncCheck ]]
    then
        echo "Updates found"
        while [[ $( docker ps --filter "ancestor=$containerName" --format "{{.ID}}" | wc -l ) -gt 0 ]]
        do
            slowlyCloseServers
        done
        #
        #start the Update
        updateEcho
    else
        echo "no Updates found"
    fi
        
    echo $rsyncCheck
}



checkForUpdates
