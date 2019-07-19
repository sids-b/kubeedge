package grpcserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kubeedge/beehive/pkg/core/model"
	"github.com/kubeedge/kubeedge/cloud/pkg/cloudhub/channelq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"io"
	"strings"
	"time"

	emodel "github.com/kubeedge/kubeedge/cloud/pkg/cloudhub/common/model"
	"github.com/kubeedge/kubeedge/cloud/pkg/cloudhub/common/util"
	"github.com/kubeedge/kubeedge/common/grpc/message"
	"github.com/kubeedge/kubeedge/common/grpc/translator"
	"net"
)

var queue *channelq.ChannelEventQueue

// StartCloudHub starts the cloud hub service
func StartCloudHub(config *util.Config, eventq *channelq.ChannelEventQueue) error {
	lis, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", 10002))
	if err != nil {
		fmt.Printf("failed to listen: %v", err)
	}
	var opts []grpc.ServerOption
	creds, err := credentials.NewServerTLSFromFile("/etc/kubeedge/certs/edge.crt", "/etc/kubeedge/certs/edge.key")
	if err != nil {
		fmt.Printf("Failed to generate credentials %v", err)
		return nil
	}
	opts = []grpc.ServerOption{grpc.Creds(creds)}
	queue = eventq
	grpcServer := grpc.NewServer(opts...)
	message.RegisterRouteMessageServer(grpcServer, &GRPCServer{})
	grpcServer.Serve(lis)

	return nil
}

type GRPCServer struct {
}

func (grpc GRPCServer) RouteMessages(stream message.RouteMessage_RouteMessagesServer) error {
	md, ok := metadata.FromIncomingContext(stream.Context())
	if !ok {
		return errors.New("failed to get context")
	}
	projectIDArray := md.Get(emodel.ProjectID)
	nodeIDarray := md.Get(emodel.NodeID)

	projectID := projectIDArray[0]
	nodeID := nodeIDarray[0]
	info := &emodel.HubInfo{ProjectID: projectID, NodeID: nodeID}
	queue.Connect(info)
	queue.Publish(info, constructConnectEvent(info, true))
	go startConsuming(info, stream)
	eventset, err := queue.Consume(info)
	if err != nil {
		return nil
	}
	for {
		event, err := eventset.Get()
		if err != nil {
			return nil
		}
		pb := &message.Message{}
		pb.Header = &message.MessageHeader{}
		pb.Router = &message.MessageRouter{}
		msg := emodel.EventToMessage(event)
		trimMessage(&msg)
		translator.NewTranslator().ModelToProto(&msg, pb)
		if err := stream.Send(pb); err != nil {
			return err
		}
	}

	return nil
}

func trimMessage(msg *model.Message) {
	resource := msg.GetResource()
	if strings.HasPrefix(resource, emodel.ResNode) {
		tokens := strings.Split(resource, "/")
		if len(tokens) < 3 {

		} else {
			msg.SetResourceOperation(strings.Join(tokens[2:], "/"), msg.GetOperation())
		}
	}
}
func startConsuming(info *emodel.HubInfo, stream message.RouteMessage_RouteMessagesServer) {
	for {
		in, err := stream.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
		msg := &model.Message{}
		translator.NewTranslator().ProtoToModel(in, msg)

		if msg.GetOperation() == emodel.OpKeepalive {
			return
		}
		msg.SetResourceOperation(fmt.Sprintf("node/%s/%s", info.NodeID, msg.GetResource()), msg.GetOperation())
		event := emodel.MessageToEvent(msg)
		if event.IsFromEdge() {
			err := queue.Publish(info, &event)
			if err != nil {
				return
			}
		}
	}
}

func constructConnectEvent(info *emodel.HubInfo, isConnected bool) *emodel.Event {
	connected := emodel.OpConnect
	if !isConnected {
		connected = emodel.OpDisConnect
	}
	body := map[string]interface{}{
		"event_type": connected,
		"timestamp":  time.Now().Unix(),
		"client_id":  info.NodeID}
	content, _ := json.Marshal(body)
	msg := model.NewMessage("")
	return &emodel.Event{
		Group:  emodel.GpResource,
		Source: emodel.SrcCloudHub,
		UserGroup: emodel.UserGroupInfo{
			Resource:  emodel.NewResource(emodel.ResNode, info.NodeID, nil),
			Operation: connected,
		},
		ID:        msg.GetID(),
		ParentID:  msg.GetParentID(),
		Timestamp: msg.GetTimestamp(),
		Content:   string(content),
	}
}
