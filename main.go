package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awsutil"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/hashicorp/consul/api"
)

type ec2MetaData struct {
	PrivateIP        string `json:"privateIp"`
	AvailabilityZone string `json:"availabilityZone"`
	InstanceID       string `json:"instanceId"`
	Region           string `json:"region"`
}

const ec2MetadataURI = "http://169.254.169.254/latest/dynamic/instance-identity/document"

func getMetadata() ec2MetaData {

	resp, err := http.Get(ec2MetadataURI)
	if err != nil {
		panic(err)
	}

	out, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		panic(err)
	}

	var metadata ec2MetaData

	err = json.Unmarshal(out, &metadata)
	if err != nil {
		panic(err)
	}
	return metadata
}

type eIPManager struct {
	metadata     ec2MetaData
	allocationID string
	environment  string
	role         string
	svc          *ec2.EC2
	client       *api.Client
	lock         *api.Lock
}

func (e *eIPManager) aquiredLock(prefix string) {
	kv := e.client.KV()

	for {
		potentialKeys, _, err := kv.List(prefix, &api.QueryOptions{})
		if err != nil {
			panic(err)
		}

		for i := range rand.Perm(len(potentialKeys)) {
			k := potentialKeys[i]
			lockKey := fmt.Sprintf("lock/%s", k.Key)
			lk, _, err := kv.Get(lockKey, nil)
			if lk != nil {
				if lk.Session != "" {
					// There is already a lock on this key
					continue
				}
			}
			l, err := e.client.LockKey(lockKey)
			if err != nil {
				panic(err)
			}
			_, err = l.Lock(nil)
			if err != nil {
				panic(err)
			} else {
				log.Printf("Aquired lock on key %s", k.Key)
				e.lock = l
				e.allocationID = string(k.Value)
				return
			}
		}
		time.Sleep(3)
	}
}

func (e *eIPManager) releaseLock() {
	e.lock.Unlock()
}

func (e *eIPManager) isAssociated() bool {

	params := &ec2.DescribeAddressesInput{
		AllocationIDs: []*string{aws.String(e.allocationID)},
	}

	resp, err := e.svc.DescribeAddresses(params)
	if err != nil {
		panic(err)
	}

	if len(resp.Addresses) == 0 {
		panic(fmt.Sprintf("Could not find EIP with AllocationID %s", e.allocationID))
	}

	address := resp.Addresses[0]
	if address.AssociationID != aws.String("") {
		return false
	}
	return true
}

func (e *eIPManager) associate() {
	params := &ec2.AssociateAddressInput{
		AllocationID:       aws.String(e.allocationID),
		AllowReassociation: aws.Boolean(true),
		InstanceID:         aws.String(e.metadata.InstanceID),
	}

	resp, err := e.svc.AssociateAddress(params)

	if err != nil {
		panic(err)
	}
	log.Printf("%s", awsutil.StringValue(resp))
}

func (e *eIPManager) disaccociate() {
	params := &ec2.DisassociateAddressInput{
		AssociationID: aws.String(e.allocationID),
	}

	resp, err := e.svc.DisassociateAddress(params)
	if err != nil {
		log.Printf("%v", err)
	}
	log.Printf("%s", awsutil.StringValue(resp))
}

func main() {
	var prefix string
	flag.StringVar(&prefix, "prefix", "nginx/eip/", "Consul key prefix")
	flag.Parse()

	meta := getMetadata()

	c, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		panic(err)
	}

	a := ec2.New(
		&aws.Config{
			Region: meta.Region,
		})

	m := &eIPManager{
		metadata: meta,
		client:   c,
		svc:      a,
	}

	m.aquiredLock(prefix)
	if m.isAssociated() {
		m.disaccociate()
	}
	m.associate()
}
