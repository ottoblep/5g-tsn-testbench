package main

import (
	"fmt"
	"github.com/facebook/time/ptp/protocol"
	"net"
	"time"
	"sync"
	"unsafe"
)

// Variables shared by the listeners
var (
	gtp_tun_opponent_addr_string string
	enable_unicast               bool
	enable_twostep               bool
	port_interface_name          string
	unicast_addr_string          string

	last_sync_residence_time           protocol.Correction
	last_sync_residence_time_mutex     sync.Mutex
	last_delayreq_residence_time       protocol.Correction
	last_delayreq_residence_time_mutex sync.Mutex
)

func ListenIncoming(listen_conn *net.UDPConn, fivegs_conn *net.UDPConn, fivegs_addr *net.UDPAddr) {
	var b []byte

	for {
		b = make([]byte, 1024)
		_, _, err := listen_conn.ReadFrom(b)

		if err != nil {
			fmt.Println(err.Error())
			continue
		}

		_, b := HandlePacket(true, b)

		_, err = fivegs_conn.WriteToUDP(b, fivegs_addr)
		if err != nil {
			fmt.Println(err.Error())
		}
	}
}

func ListenOutgoingUnicast(fivegs_conn *net.UDPConn, unicast_general_conn *net.UDPConn, unicast_event_conn *net.UDPConn, unicast_general_addr *net.UDPAddr, unicast_event_addr *net.UDPAddr) {
	var msg_type protocol.MessageType
	var b []byte

	for {
		b = make([]byte, 1024)
		_, _, err := fivegs_conn.ReadFromUDP(b)

		msg_type, b = HandlePacket(false, b)

		switch msg_type {
		// Outgoing split by: port 320 or 319
		case protocol.MessageSync, protocol.MessageDelayReq, protocol.MessagePDelayReq, protocol.MessagePDelayResp: // Port 319 event
			{
				_, err = unicast_event_conn.WriteToUDP(b, unicast_event_addr)
			}
		case protocol.MessageAnnounce, protocol.MessageFollowUp, protocol.MessageDelayResp, protocol.MessageSignaling, protocol.MessageManagement, protocol.MessagePDelayRespFollowUp: // Port 320 general
			{
				_, err = unicast_general_conn.WriteToUDP(b, unicast_general_addr)
			}
		default:
			{
				fmt.Println("TT: dropping unknown type or non-PTP packet")
			}
		}

		if err != nil {
			fmt.Println(err.Error())
		}
	}
}

func ListenOutgoingMulticast(fivegs_conn *net.UDPConn,
	peer_general_multicast_conn *net.UDPConn, peer_event_multicast_conn *net.UDPConn,
	non_peer_general_multicast_conn *net.UDPConn, non_peer_event_multicast_conn *net.UDPConn,
	peer_general_addr *net.UDPAddr, peer_event_addr *net.UDPAddr,
	non_peer_general_addr *net.UDPAddr, non_peer_event_addr *net.UDPAddr) {

	var msg_type protocol.MessageType
	var b []byte

	for {
		b = make([]byte, 1024)
		_, _, err := fivegs_conn.ReadFromUDP(b)

		msg_type, b = HandlePacket(false, b)

		switch msg_type {
		// Outgoing split by: port 320 or 319, multicast 0.107 or 1.129
		case protocol.MessageSync, protocol.MessageDelayReq: // Port 319 event, 224.0.1.129 non-peer
			{
				_, err = non_peer_event_multicast_conn.WriteToUDP(b, non_peer_event_addr)
			}
		case protocol.MessagePDelayReq, protocol.MessagePDelayResp: // Port 319 event, 224.0.0.107 peer
			{
				_, err = peer_event_multicast_conn.WriteToUDP(b, peer_event_addr)
			}
		case protocol.MessageAnnounce, protocol.MessageFollowUp, protocol.MessageDelayResp, protocol.MessageSignaling, protocol.MessageManagement: // Port 320 general, 224.0.1.129 non-peer
			{
				_, err = non_peer_general_multicast_conn.WriteToUDP(b, non_peer_general_addr)
			}
		case protocol.MessagePDelayRespFollowUp: // Port 320 general, 224.0.0.107 peer
			{
				_, err = peer_general_multicast_conn.WriteToUDP(b, peer_general_addr)
			}
		default:
			{
				fmt.Println("TT: dropping unknown type or non-PTP packet")
			}
		}

		if err != nil {
			fmt.Println(err.Error())
		}
	}
}

func HandlePacket(incoming bool, raw_pkt []byte) (protocol.MessageType, []byte) {
	// Act as transparent clock
	// We want to support both two step and one step transparent clock operation
	// so we both update the Sync/DelayRequest correction fields directly (1-step) and store the residence for a possible FollowUp or DelayResponse (2-step)
	// Peer to peer mode is not supported

	// Attempt to parse possible PTP packet
	parsed_pkt, err := protocol.DecodePacket(raw_pkt)
	zero_correction := protocol.NewCorrection(0)
	if err != nil {
		fmt.Println(err.Error())
		return 255, raw_pkt
	}

	// Type switch into ptp packet types
	switch pkt_ptr := parsed_pkt.(type) {
	case *protocol.SyncDelayReq:
		{
			correction := CalculateCorrection(incoming, (*pkt_ptr).Header.CorrectionField)

			if enable_twostep && !incoming {
				// In two step mode the follow up / delay response communicate the residence time
				(*pkt_ptr).Header.CorrectionField = zero_correction
			} else {
				(*pkt_ptr).Header.CorrectionField = correction
			}

			if !incoming {
				if (*pkt_ptr).Header.MessageType() == protocol.MessageSync {
					last_sync_residence_time_mutex.Lock()
					last_sync_residence_time = correction
					last_sync_residence_time_mutex.Unlock()
				} else {
					last_delayreq_residence_time_mutex.Lock()
					last_delayreq_residence_time = correction
					last_delayreq_residence_time_mutex.Unlock()
				}
			}
			raw_pkt, err = (*pkt_ptr).MarshalBinary()
		}
	case *protocol.FollowUp:
		{
			if !incoming {
				(*pkt_ptr).Header.CorrectionField = last_sync_residence_time
				raw_pkt, err = (*pkt_ptr).MarshalBinary()
			}
		}
	case *protocol.DelayResp:
		{
			if incoming {
				(*pkt_ptr).Header.CorrectionField = last_delayreq_residence_time
				raw_pkt, err = (*pkt_ptr).MarshalBinary()
			}
		}
	}

	if err != nil {
		fmt.Println(err.Error())
	}

	return parsed_pkt.MessageType(), raw_pkt
}

func CalculateCorrection(incoming bool, correctionField protocol.Correction) protocol.Correction {
	// We hijack the 64bit correction field for temporarily storing the ingress time
	// Then we overwrite the elapsed time with the residence time at the egress port
	// Normally this is done by appending a suffix to the ptp message
	// TODO: This makes it impossible to chain different bridges and accumulate corrections

	if incoming {
		return UnixNanoToCorrection(time.Now().UnixNano())
	} else {
		residence_time := float64(time.Now().UnixNano() - CorrectionToUnixNano(correctionField))
		if residence_time <= 0 {
			fmt.Println("TT: calculated nonsense residence time ", residence_time, ", are the tt's clocks synchronized?")
			residence_time = 0
		}
		return protocol.NewCorrection(residence_time)
	}
}

// These functions do not convert between the two types! They are used to store a unix nanosecond in the header of the ptp message which has type protocol.Correction
// This unsafe casting works because the protocol.Correction type and unix nanoseconds types are both 64bit
func UnixNanoToCorrection(f int64) protocol.Correction {
	return *(*protocol.Correction)(unsafe.Pointer(&f))
}

func CorrectionToUnixNano(f protocol.Correction) int64 {
	return *(*int64)(unsafe.Pointer(&f))
}
