package cluster_files

import (
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/pkg/errors"
	"github.com/submariner-io/admiral/pkg/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog"
)

// Get retrieves a config from a secret, configmap or file within the k8s cluster
// using an url schema that supports configmap://<namespace>/<configmap-name>/<data-file>
// secret://<namespace>/<secret-name>/<data-file> and file:///<path> returning
// a local path to the file
func Get(k8sClient kubernetes.Interface, urlAddress string) (pathStr string, err error) {
	klog.V(log.DEBUG).Infof("reading cluster_file: %s", urlAddress)
	parsedUrl, err := url.Parse(urlAddress)
	if err != nil {
		return "", errors.Wrapf(err, "error parsing cluster file URL %q", urlAddress)
	}

	namespace := parsedUrl.Host
	pathContainerObject, pathFile := path.Split(parsedUrl.Path)
	pathContainerObject = strings.Trim(pathContainerObject, "/")

	if pathContainerObject == "" || pathFile == "" {
		return "", errors.Errorf("cluster file URL %q is not well formed", urlAddress)
	}

	var data []byte

	switch parsedUrl.Scheme {
	case "file":
		return parsedUrl.Path, nil

	case "secret":
		secret, err := k8sClient.CoreV1().Secrets(namespace).Get(pathContainerObject, metav1.GetOptions{})
		if err != nil {
			return "", errors.Wrapf(err, "error reading secret %q from namespace %q", pathContainerObject, namespace)
		}
		var ok bool
		data, ok = secret.Data[pathFile]
		if !ok {
			return "", errors.Errorf("cluster file data %q not found in secret %s", pathFile, secret.Name)
		}

	case "configmap":
		configMap, err := k8sClient.CoreV1().ConfigMaps(namespace).Get(pathContainerObject, metav1.GetOptions{})
		if err != nil {
			return "", errors.Wrapf(err, "error reading configmap %q from namespace %q", pathContainerObject, namespace)
		}
		var ok bool
		data, ok = configMap.BinaryData[pathFile]
		if !ok {
			dataStr, ok := configMap.Data[pathFile]
			if !ok {
				return "", errors.Errorf("cluster file data %q not found in %#v", pathFile, configMap)
			}

			data = []byte(dataStr)
		}

	default:
		return "", errors.Errorf("the scheme %q in cluster file URL %q is not supported ", parsedUrl.Scheme, urlAddress)
	}

	return storeToDisk(pathContainerObject, parsedUrl, data)
}

func storeToDisk(pathContainerObject string, parsedUrl *url.URL, data []byte) (string, error) {
	storageDirectory, err := ioutil.TempDir("", "cluster_files")
	if err != nil {
		return "", errors.Wrap(err, "error creating cluster_files directory")
	}

	diskFilePath := path.Join(storageDirectory, parsedUrl.Path)
	dir := path.Join(storageDirectory, pathContainerObject)
	err = os.MkdirAll(dir, 0700)
	if err != nil {
		return "", errors.Wrapf(err, "error creating %s directory to store %s", dir, diskFilePath)
	}

	err = ioutil.WriteFile(diskFilePath, data, 0400)
	if err != nil {
		klog.Error(err)
		return "", errors.Wrapf(err, "error writing cluster file to  %q", diskFilePath)
	}

	return diskFilePath, nil
}
