#!/usr/bin/env bash
set -e

function kind_clusters() {
    for i in 1 2 3; do
        if [[ $(kind get clusters | grep cluster${i} | wc -l) -gt 0  ]]; then
            echo Cluster clister${i} already exists, skipping cluster creation...
        else
            if [[ -n "$2" ]]; then
                kind create cluster --image=kindest/node:v$2 --name=cluster${i} --wait=5m --config=./kind-e2e/cluster${i}-config.yaml
            else
                kind create cluster --name=cluster${i} --wait=5m --config=./kind-e2e/cluster${i}-config.yaml
            fi
            master_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' cluster${i}-control-plane | head -n 1)
            sed -i -- "s/user: kubernetes-admin/user: cluster$i/g" $(kind get kubeconfig-path --name="cluster$i")
            sed -i -- "s/name: kubernetes-admin.*/name: cluster$i/g" $(kind get kubeconfig-path --name="cluster$i")

            if [[ $1 = keep ]]; then
                cp -r $(kind get kubeconfig-path --name="cluster$i") ../output/kind-config/local-dev/kind-config-cluster${i}
            fi

            sed -i -- "s/server: .*/server: https:\/\/$master_ip:6443/g" $(kind get kubeconfig-path --name="cluster$i")
            cp -r $(kind get kubeconfig-path --name="cluster$i") ../output/kind-config/dapper/kind-config-cluster${i}
            export KUBECONFIG=$(kind get kubeconfig-path --name=cluster1):$(kind get kubeconfig-path --name=cluster2):$(kind get kubeconfig-path --name=cluster3)
        fi
    done
}

function install_helm() {
    helm init --client-only
    helm repo add submariner-latest https://releases.rancher.com/submariner-charts/latest
    for i in 1 2 3; do
        kubectl config use-context cluster${i}
        if kubectl -n kube-system rollout status deploy/tiller-deploy > /dev/null 2>&1; then
            echo Helm already installed on clister${i}, skipping helm installation...
        else
            echo Installing helm on clister${i}.
            kubectl -n kube-system create serviceaccount tiller
            kubectl create clusterrolebinding tiller --clusterrole=cluster-admin --serviceaccount=kube-system:tiller
            helm init --service-account tiller
            kubectl -n kube-system  rollout status deploy/tiller-deploy
        fi
    done
}

function setup_broker() {
    kubectl config use-context cluster1
    if kubectl get crd clusters.submariner.io > /dev/null 2>&1; then
        echo Submariner CRDs already exist, skipping broker creation...
    else
        echo Installing broker on cluster1.
        helm install submariner-latest/submariner-k8s-broker --name ${SUBMARINER_BROKER_NS} --namespace ${SUBMARINER_BROKER_NS}
    fi

    SUBMARINER_BROKER_URL=$(kubectl -n default get endpoints kubernetes -o jsonpath="{.subsets[0].addresses[0].ip}:{.subsets[0].ports[?(@.name=='https')].port}")
    SUBMARINER_BROKER_CA=$(kubectl -n ${SUBMARINER_BROKER_NS} get secrets -o jsonpath="{.items[?(@.metadata.annotations['kubernetes\.io/service-account\.name']=='${SUBMARINER_BROKER_NS}-client')].data['ca\.crt']}")
    SUBMARINER_BROKER_TOKEN=$(kubectl -n ${SUBMARINER_BROKER_NS} get secrets -o jsonpath="{.items[?(@.metadata.annotations['kubernetes\.io/service-account\.name']=='${SUBMARINER_BROKER_NS}-client')].data.token}"|base64 --decode)
}

function setup_cluster2_gateway() {
    kubectl config use-context cluster2
    if kubectl wait --for=condition=Ready pods -l app=submariner-engine -n submariner --timeout=60s > /dev/null 2>&1; then
            echo Submariner already installed, skipping submariner helm installation...
            update_subm_pods
        else
            worker_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' cluster2-worker | head -n 1)
            kubectl label node cluster2-worker "submariner.io/gateway=true" --overwrite
            helm install submariner-latest/submariner \
            --name submariner \
            --namespace submariner \
            --set ipsec.psk="${SUBMARINER_PSK}" \
            --set broker.server="${SUBMARINER_BROKER_URL}" \
            --set broker.token="${SUBMARINER_BROKER_TOKEN}" \
            --set broker.namespace="${SUBMARINER_BROKER_NS}" \
            --set broker.ca="${SUBMARINER_BROKER_CA}" \
            --set submariner.clusterId="cluster2" \
            --set submariner.clusterCidr="$worker_ip/32" \
            --set submariner.serviceCidr="100.95.0.0/16" \
            --set submariner.natEnabled="false" \
            --set routeAgent.image.repository="submariner-route-agent" \
            --set routeAgent.image.tag="local" \
            --set routeAgent.image.pullPolicy="IfNotPresent" \
            --set engine.image.repository="submariner" \
            --set engine.image.tag="local" \
            --set engine.image.pullPolicy="IfNotPresent"
            echo Waiting for submariner pods to be Ready on cluster2...
            kubectl wait --for=condition=Ready pods -l app=submariner-engine -n submariner --timeout=60s
            kubectl wait --for=condition=Ready pods -l app=submariner-routeagent -n submariner --timeout=60s
            echo Deploying netshoot on cluster2 worker: ${worker_ip}
            kubectl apply -f ./kind-e2e/netshoot.yaml
            echo Waiting for netshoot pods to be Ready on cluster2.
            kubectl rollout status deploy/netshoot --timeout=120s
    fi
}

function setup_cluster3_gateway() {
    kubectl config use-context cluster3
    if kubectl wait --for=condition=Ready pods -l app=submariner-engine -n submariner --timeout=60s > /dev/null 2>&1; then
            echo Submariner already installed, skipping submariner helm installation...
            update_subm_pods
        else
            worker_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' cluster3-worker | head -n 1)
            kubectl label node cluster3-worker "submariner.io/gateway=true" --overwrite
            helm install submariner-latest/submariner \
             --name submariner \
             --namespace submariner \
             --set ipsec.psk="${SUBMARINER_PSK}" \
             --set broker.server="${SUBMARINER_BROKER_URL}" \
             --set broker.token="${SUBMARINER_BROKER_TOKEN}" \
             --set broker.namespace="${SUBMARINER_BROKER_NS}" \
             --set broker.ca="${SUBMARINER_BROKER_CA}" \
             --set submariner.clusterId="cluster3" \
             --set submariner.clusterCidr="$worker_ip/32" \
             --set submariner.serviceCidr="100.96.0.0/16" \
             --set submariner.natEnabled="false" \
             --set routeAgent.image.repository="submariner-route-agent" \
             --set routeAgent.image.tag="local" \
             --set routeAgent.image.pullPolicy="IfNotPresent" \
             --set engine.image.repository="submariner" \
             --set engine.image.tag="local" \
             --set engine.image.pullPolicy="IfNotPresent"
            echo Waiting for submariner pods to be Ready on cluster3...
            kubectl wait --for=condition=Ready pods -l app=submariner-engine -n submariner --timeout=60s
            kubectl wait --for=condition=Ready pods -l app=submariner-routeagent -n submariner --timeout=60s
            echo Deploying nginx on cluster3 worker: ${worker_ip}
            kubectl apply -f ./kind-e2e/nginx-demo.yaml
            echo Waiting for nginx-demo deployment to be Ready on cluster3.
            kubectl rollout status deploy/nginx-demo --timeout=120s
    fi
}

function kind_import_images() {
    docker tag rancher/submariner:dev submariner:local
    docker tag rancher/submariner-route-agent:dev submariner-route-agent:local

    for i in 2 3; do
        echo "Loading submariner images in to cluster${i}..."
        kind --name cluster${i} load docker-image submariner:local
        kind --name cluster${i} load docker-image submariner-route-agent:local
    done
}

function test_connection() {
    kubectl config use-context cluster3
    nginx_svc_ip_cluster3=$(kubectl get svc -l app=nginx-demo | awk 'FNR == 2 {print $3}')
    kubectl config use-context cluster2
    netshoot_pod=$(kubectl get pods -l app=netshoot | awk 'FNR == 2 {print $1}')

    echo "Testing connectivity between clusters - $netshoot_pod cluster2 --> $nginx_svc_ip_cluster3 nginx service cluster3"

    attempt_counter=0
    max_attempts=5
    until $(kubectl exec -it ${netshoot_pod} -- curl --output /dev/null --silent --head --fail ${nginx_svc_ip_cluster3}); do
        if [ ${attempt_counter} -eq ${max_attempts} ];then
          echo "Max attempts reached, connection failed!"
          exit 1
        fi

        attempt_counter=$(($attempt_counter+1))
        sleep 10
    done
    echo "Connection test was successful!"
}

function update_subm_pods() {
    echo Removing submariner engine pods...
    kubectl get pods -n submariner -l app=submariner-engine | awk 'FNR > 1 {print $1}' | xargs kubectl delete pods -n submariner
    kubectl wait --for=condition=Ready pods -l app=submariner-engine -n submariner --timeout=60s
    echo Removing submariner route agent pods...
    kubectl get pods -n submariner -l app=submariner-routeagent | awk 'FNR > 1 {print $1}' | xargs kubectl delete pods -n submariner
    kubectl wait --for=condition=Ready pods -l app=submariner-routeagent -n submariner --timeout=60s
}

function enable_logging() {
    kubectl config use-context cluster1
    if kubectl rollout status deploy/kibana > /dev/null 2>&1; then
        echo Elasticsearch stack already installed, skipping...
    else
        echo Installing Elasticsearch...
        es_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' cluster1-control-plane | head -n 1)
        kubectl apply -f ./kind-e2e/logging/elasticsearch.yaml
        kubectl apply -f ./kind-e2e/logging/filebeat.yaml
        echo Waiting for Elasticsearch to be ready...
        kubectl wait --for=condition=Ready pods -l app=elasticsearch --timeout=300s
        for i in 2 3; do
            kubectl config use-context cluster${i}
            kubectl apply -f ./kind-e2e/logging/filebeat.yaml
            kubectl set env daemonset/filebeat -n kube-system ELASTICSEARCH_HOST=${es_ip} ELASTICSEARCH_PORT=30000
        done
    fi
}

function cleanup {
  for i in 1 2 3; do

    if [[ $(kind get clusters | grep cluster${i} | wc -l) -gt 0  ]]; then
        kind delete cluster --name=cluster${i};
    fi
    done

    if [[ $(docker ps -qf status=exited | wc -l) -gt 0 ]]; then
        echo Cleaning containers...
        docker ps -qf status=exited | xargs docker rm -f
    fi
    if [[ $(docker images -qf dangling=true | wc -l) -gt 0 ]]; then
        echo Cleaning images...
        docker images -qf dangling=true | xargs docker rmi -f
    fi
#    if [[ $(docker images -q --filter=reference='submariner*:local' | wc -l) -gt 0 ]]; then
#        docker images -q --filter=reference='submariner*:local' | xargs docker rmi -f
#    fi
    if [[ $(docker volume ls -qf dangling=true | wc -l) -gt 0 ]]; then
        echo Cleaning volumes...
        docker volume ls -qf dangling=true | xargs docker volume rm -f
    fi
}

if [[ $1 = clean ]]; then
    cleanup
    exit 0
fi

if [[ $1 != keep ]]; then
    trap cleanup EXIT
fi

echo Starting a job with status: $1, k8s version: $2, monitor: $3.
mkdir -p ../output/kind-config/dapper/ ../output/kind-config/local-dev/
SUBMARINER_BROKER_NS=submariner-k8s-broker
SUBMARINER_PSK=$(cat /dev/urandom | LC_CTYPE=C tr -dc 'a-zA-Z0-9' | fold -w 64 | head -n 1)
export KUBECONFIG=../output/kind-config/dapper/kind-config-cluster1:../output/kind-config/dapper/kind-config-cluster2:../output/kind-config/dapper/kind-config-cluster3

kind_clusters "$@"
kind_import_images
install_helm
if [[ $3 = true ]]; then
    enable_logging
fi
setup_broker
setup_cluster2_gateway
setup_cluster3_gateway
test_connection
