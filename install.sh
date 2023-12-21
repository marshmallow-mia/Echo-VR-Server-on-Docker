#!bin/bash
#This script is used to install docker, build the container and configure everything

configJson='{
  "apiservice_host": "http://_IP:_PORT/api",
  "configservice_host": "ws://_IP:_PORT/config",
  "loginservice_host": "ws://_IP:_PORT/login?auth=_PW&displayname=_NAME",
  "matchingservice_host": "ws://_IP:_PORT/matching",
  "serverdb_host": "ws://_IP:_PORT/serverdb?api_key=_API",
  "transactionservice_host": "ws://_IP:_PORT/transaction",
  "publisher_lock": "rad15_live"
}'


#This function checks if the echo folder is available
function checkForEchoFolder {
    if ! [ -d "./ready-at-dawn-echo-arena" ]
    then
        scriptPath="$(dirname -- "${BASH_SOURCE[0]}")"
        echo "No ready-at-dawn-echo-arena-Folder found. It needs to be in the same folder like this script: $PWD"
        exit
    fi
    
}


#This function gets the needed Input from the user per stdin and does some basic testing
function getNeededParameterFromSTDin {
    echo -e '\033[0;31m' #write in red
    #Get parameters:
    echo "Please enter the IP-Address and Port of the Relay-Server you want to get connected to:"
    read ip
    echo "Please enter your username for connecting to the server:"
    read name
    echo "Please enter your password (hidden):"
    read -s password
    echo "Please enter the API-Key (hidden):"
    read -s api
    
    #check if every parameter is given, if not start again
    if [ "$ip"  == "" ] ||  [ "$name"  == "" ] ||  [ "$password"  == "" ] ||  [ "$api"  == "" ]
    then
        echo "At least one of the entered Parameters is empty"
        #If something empty, start again
        getNeededParameterFromSTDin
        return 10
    fi
    
    #Ask if the parameters are correct
    echo "Are these Parameters correct?"
    echo "IP:Port: "$ip
    echo "Name: "$name
    echo "Enter y/Y for Yes, anything else for No"
    read answer
    
    #If not correct, start again
    if ! [ "$answer" == "y" ] || [ "$answer" == "Y" ]
    then
        getNeededParameterFromSTDin
        return 11
         
    fi
}


function getRegion {
    echo -e '\033[0;31m' #write in red
    echo 'Please enteryour Region:
    "uscn", // US Central North (Chicago)
    "us-central-2", // US Central South (Texas)
    "us-central-3", // US Central South (Texas)
    "use", // US East (Virgina)
    "usw", // US West (California)
    "euw", // EU West 
    "jp", // Japan (idk)
    "sin", // Singapore oce region
    '
    read region
    echo $region
    if ! [ "$region" = "uscn" ] && ! [ "$region" = "us-central-2" ] && ! [ "$region" = "us-central-3" ] && ! [ "$region" = "use" ] && ! [ "$region" = "usw" ] && ! [ "$region" = "euw" ] && ! [ "$region" = "jp" ] && ! [ "$region" = "sin" ]
    then
        echo "Wrong Region. Please enter again:"
        getRegion
        return 15
    fi   
    
}


#this function checks and writes the config.json
function writeConfigFile {
    echo -e '\033[0;31m' #write in red
    echo "Do you want to see the config.json before we write it? Password and API-Key will be visible. Enter y/Y for Yes, anything else for No."
    read askCheck
    if [ "$askCheck" == "y" ] || [ "$askCheck" == "Y" ]
    then
        
        echo "$(echo "$configJson" | sed -e "s/_IP:_PORT/$ip/g" -e "s/_PW/$password/g" -e "s/_NAME/$name/g" -e "s/_API/$api/g")"
        #echo "$configJson"
        echo "Is this config correct? If you don't Enter y/Y we will ask you the Parameters again"
        read askCorrect
        if ! [ "$askCorrect" == "y" ] || [ "$askCorrect" == "Y" ]
        then
            getNeededParameterFromSTDin
            writeConfigFile
            return 12
        fi
    fi
    echo "The installation of Docker will now start"
    sleep 3
    mv ./ready-at-dawn-echo-arena/_local/config.json ./ready-at-dawn-echo-arena/_local/config.json_backup
    echo "$(echo "$configJson" | sed -e "s/_IP:_PORT/$ip/g" -e "s/_PW/$password/g" -e "s/_NAME/$name/g" -e "s/_API/$api/g")" > ./ready-at-dawn-echo-arena/_local/config.json
}



#change the region
function writeRegion {
    sed -i -e "s/region='.*'/region='$region'/g" ./scripts/start-echo.sh    
}


#this function checks for the OS
function checkOS {
    echo -e '\033[0;31m' #write in red
    if [ $(grep -c Debian /etc/os-release ) -gt 0 ] || [ $(grep -c Ubuntu /etc/os-release ) -gt 0 ]
    then
        osRelease=deb
    elif [ $(grep -c Fedora /etc/os-release ) -gt 0 ]
    then
        osRelease=fed  
    elif [ $(grep -c CentOS /etc/os-release ) -gt 0 ]
    then
        osRelease=cen     
    elif [ $(grep -c Manjaro /etc/os-release ) -gt 0 ] || [ $(grep -c arch /etc/os-release ) -gt 0 ] || [ $(grep -c Arch /etc/os-release ) -gt 0 ]
    then
        osRelease=arc
    else
        echo "Unable to find out which Distro you use.
        Please enter one of the following numbers:
        1. Debian/Ubuntu
        2. Fedora
        3. CentOS
        4. Arch Linux
        "
        read askDistro
        if [ $askDistro -eq 1 ]
        then
            osRelease=deb
        elif [ $askDistro -eq 2 ]
        then
            osRelease=fed  
        elif [ $askDistro -eq 3 ]
        then
            osRelease=cen     
        elif [ $askDistro -eq 4 ]
        then
            osRelease=arc
        else
            echo "Wrong Input, try again."
            checkOS
            return 13
        fi   
    fi
}


#This function installs Docker
function installNeededSoftware {
    if [ "$osRelease" = "deb" ]
    then
        # Add Docker's official GPG key:
        apt update
        apt install -y ca-certificates curl gnupg
        install -m 0755 -d /etc/apt/keyrings
        curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
        chmod a+r /etc/apt/keyrings/docker.gpg
        
        # Add the repository to Apt sources:
        echo \
          "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian \
          $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
          tee /etc/apt/sources.list.d/docker.list > /dev/null
        apt update
        apt install -y lsof docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
    elif [ "$osRelease" = "fed" ]
    then
        #remove old Docker Versions
        dnf -y remove docker \
                  docker-client \
                  docker-client-latest \
                  docker-common \
                  docker-latest \
                  docker-latest-logrotate \
                  docker-logrotate \
                  docker-selinux \
                  docker-engine-selinux \
                  docker-engine
        #add repo
        dnf -y install dnf-plugins-core
        dnf -y config-manager --add-repo https://download.docker.com/linux/fedora/docker-ce.repo
        #install docker and lsof
        dnf -y install lsof docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
        #start and enable docker
        systemctl start docker
        systemctl enable docker
    elif [ "$osRelease" = "cen" ]
    then
        #remove old Docker Versions
        yum -y remove docker \
                  docker-client \
                  docker-client-latest \
                  docker-common \
                  docker-latest \
                  docker-latest-logrotate \
                  docker-logrotate \
                  docker-engine
        #add repo
        yum -y install -y yum-utils
        yum-config-manager -y --add-repo https://download.docker.com/linux/centos/docker-ce.repo
        #install docker and lsof
        yum -y install lsof docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
        #start and enable docker
        systemctl start docker
        systemctl enable docker
        
    elif [ "$osRelease" = "arc" ]
    then
        #Install Docker and lsof
        pacman --noconfirm -S docker lsof
        #Start and enable docker
        systemctl start docker
        systemctl enable docker
    fi
}


#build the container
function buildPackage {
    docker build -t ready-at-dawn-echo-arena .    
}


#This function starts the amount of wanted container
function startContainer {
    echo -e '\033[0;31m' #write in red
    #Ask for the amount
    echo "If you want to start the containers now, enter the amount, otherwise enter 0. (They will automaticaly restart after system reboot if you dont stop them with \"docker stop <Container-ID>\")"
    read askAmount
    
    #Make sure it is correct
    echo "We will start $askAmount Container
    Is that correct? Enter y/Y for Yes, anything else for No."
    read askAmountCorrect
    #If not correct, start again
    if ! [ "$askAmountCorrect" == "y" ] || [ "$askAmountCorrect" == "Y" ]
    then
        startContainer
        return 14         
    fi
    echo "We will start the Container now. This can take some time"
    
    #Start the correct amount
    if [[ "$askAmount" =~ ^[0-9]+$ ]] && ! [ "$askAmount" = 0 ]
    then
        c=0
        while [ $c -lt $askAmount ]
        do
            bash ./run.sh
            ((c++))
            sleep 2
        done
    else
        echo "Amount wasnt correct"
        startContainer
        return 15
    fi
    
    echo "The installation is done. To start a new docker container, just run the \"run.sh\" and it will do everything for you
    If you want to stop a containter run \" docker ps\" to get the ID and run \"docker stop <Container-ID>\" to stop it.
    Every running Container will stay on as long as you dont stop them manually. Even after reboot!
    If you want to change the parameters for the echo server, you can change them in ./scripts/start-echo.sh
    You will need to restart the container!
    "
}



checkForEchoFolder
getNeededParameterFromSTDin
getRegion
writeConfigFile
writeRegion
checkOS 
installNeededSoftware
buildPackage
startContainer


echo -e '\033[0m' # No Color
