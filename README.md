# k8s-admission-controller

## Running the app
### 1. Install Dependencies
- Exec `export GOPROXY=https://goproxy.io,direct`
- Exec `go mod tidy`

### 2. Run Project
- run project with follow command: `{PUT ENVs HERE} go run .`


## Prepare webhook deployment

### Create Ca Bundle
openssl base64 -A <"/opt/kube-ansible/certs/ca.pem"