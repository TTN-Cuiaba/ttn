// Copyright © 2016 The Things Network
// Use of this source code is governed by the MIT license that can be found in the LICENSE file.

package cmd

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	pb "github.com/TheThingsNetwork/ttn/api/handler"
	"github.com/TheThingsNetwork/ttn/core"
	"github.com/TheThingsNetwork/ttn/core/handler"
	"github.com/TheThingsNetwork/ttn/core/proxy"
	"github.com/apex/log"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"gopkg.in/redis.v4"
)

// handlerCmd represents the handler command
var handlerCmd = &cobra.Command{
	Use:   "handler",
	Short: "The Things Network handler",
	Long:  ``,
	PreRun: func(cmd *cobra.Command, args []string) {
		ctx.WithFields(log.Fields{
			"Server":     fmt.Sprintf("%s:%d", viper.GetString("handler.server-address"), viper.GetInt("handler.server-port")),
			"HTTP Proxy": fmt.Sprintf("%s:%d", viper.GetString("handler.http-address"), viper.GetInt("handler.http-port")),
			"Announce":   fmt.Sprintf("%s:%d", viper.GetString("handler.server-address-announce"), viper.GetInt("handler.server-port")),
			"Database":   fmt.Sprintf("%s/%d", viper.GetString("handler.redis-address"), viper.GetInt("handler.redis-db")),
			"TTNBroker":  viper.GetString("handler.ttn-broker"),
			"MQTTBroker": viper.GetString("handler.mqtt-broker"),
			"AMQPHost":   viper.GetString("handler.amqp-host"),
		}).Info("Initializing Handler")
	},
	Run: func(cmd *cobra.Command, args []string) {
		ctx.Info("Starting")

		// Redis Client
		client := redis.NewClient(&redis.Options{
			Addr:     viper.GetString("handler.redis-address"),
			Password: "", // no password set
			DB:       viper.GetInt("handler.redis-db"),
		})

		connectRedis(client)

		// Component
		component, err := core.NewComponent(ctx, "handler", fmt.Sprintf("%s:%d", viper.GetString("handler.server-address-announce"), viper.GetInt("handler.server-port")))
		if err != nil {
			ctx.WithError(err).Fatal("Could not initialize component")
		}

		// Handler
		handler := handler.NewRedisHandler(
			client,
			viper.GetString("handler.ttn-broker"),
			viper.GetString("handler.mqtt-username"),
			viper.GetString("handler.mqtt-password"),
			viper.GetString("handler.mqtt-broker"),
		)
		if viper.GetString("handler.amqp-host") != "" {
			handler = handler.WithAMQP(
				viper.GetString("handler.amqp-username"),
				viper.GetString("handler.amqp-password"),
				viper.GetString("handler.amqp-host"),
				viper.GetString("handler.amqp-exchange"))
		}
		err = handler.Init(component)
		if err != nil {
			ctx.WithError(err).Fatal("Could not initialize handler")
		}
		defer handler.Shutdown()

		// gRPC Server
		lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", viper.GetString("handler.server-address"), viper.GetInt("handler.server-port")))
		if err != nil {
			ctx.WithError(err).Fatal("Could not start gRPC server")
		}
		grpc := grpc.NewServer(component.ServerOptions()...)

		// Register and Listen
		handler.RegisterRPC(grpc)
		handler.RegisterManager(grpc)
		go grpc.Serve(lis)
		defer grpc.Stop()

		if viper.GetString("handler.http-address") != "" && viper.GetInt("handler.http-port") != 0 {
			proxyConn, err := component.Identity.Dial()
			if err != nil {
				ctx.WithError(err).Fatal("Could not start client for gRPC proxy")
			}
			mux := runtime.NewServeMux()
			netCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pb.RegisterApplicationManagerHandler(netCtx, mux, proxyConn)
			go func() {
				err := http.ListenAndServe(fmt.Sprintf("%s:%d", viper.GetString("handler.http-address"), viper.GetInt("handler.http-port")), proxy.WithToken(mux))
				if err != nil {
					ctx.WithError(err).Fatal("Error in gRPC proxy")
				}
			}()
		}

		sigChan := make(chan os.Signal)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		ctx.WithField("signal", <-sigChan).Info("signal received")

	},
}

func init() {
	RootCmd.AddCommand(handlerCmd)

	handlerCmd.Flags().String("redis-address", "localhost:6379", "Redis host and port")
	viper.BindPFlag("handler.redis-address", handlerCmd.Flags().Lookup("redis-address"))
	handlerCmd.Flags().Int("redis-db", 0, "Redis database")
	viper.BindPFlag("handler.redis-db", handlerCmd.Flags().Lookup("redis-db"))

	handlerCmd.Flags().String("ttn-broker", "dev", "The ID of the TTN Broker as announced in the Discovery server")
	viper.BindPFlag("handler.ttn-broker", handlerCmd.Flags().Lookup("ttn-broker"))

	handlerCmd.Flags().String("mqtt-broker", "localhost:1883", "MQTT broker host and port")
	viper.BindPFlag("handler.mqtt-broker", handlerCmd.Flags().Lookup("mqtt-broker"))

	handlerCmd.Flags().String("mqtt-username", "", "MQTT username")
	viper.BindPFlag("handler.mqtt-username", handlerCmd.Flags().Lookup("mqtt-username"))

	handlerCmd.Flags().String("mqtt-password", "", "MQTT password")
	viper.BindPFlag("handler.mqtt-password", handlerCmd.Flags().Lookup("mqtt-password"))

	handlerCmd.Flags().String("amqp-host", "", "AMQP host and port. Leave empty to disable AMQP")
	viper.BindPFlag("handler.amqp-host", handlerCmd.Flags().Lookup("amqp-host"))

	handlerCmd.Flags().String("amqp-username", "guest", "AMQP username")
	viper.BindPFlag("handler.amqp-username", handlerCmd.Flags().Lookup("amqp-username"))

	handlerCmd.Flags().String("amqp-password", "guest", "AMQP password")
	viper.BindPFlag("handler.amqp-password", handlerCmd.Flags().Lookup("amqp-password"))

	handlerCmd.Flags().String("amqp-exchange", "ttn.handler", "AMQP exchange")
	viper.BindPFlag("handler.amqp-exchange", handlerCmd.Flags().Lookup("amqp-exchange"))

	handlerCmd.Flags().String("server-address", "0.0.0.0", "The IP address to listen for communication")
	handlerCmd.Flags().String("server-address-announce", "localhost", "The public IP address to announce")
	handlerCmd.Flags().Int("server-port", 1904, "The port for communication")
	viper.BindPFlag("handler.server-address", handlerCmd.Flags().Lookup("server-address"))
	viper.BindPFlag("handler.server-address-announce", handlerCmd.Flags().Lookup("server-address-announce"))
	viper.BindPFlag("handler.server-port", handlerCmd.Flags().Lookup("server-port"))

	handlerCmd.Flags().String("http-address", "0.0.0.0", "The IP address where the gRPC proxy should listen")
	handlerCmd.Flags().Int("http-port", 0, "The port where the gRPC proxy should listen")
	viper.BindPFlag("handler.http-address", handlerCmd.Flags().Lookup("http-address"))
	viper.BindPFlag("handler.http-port", handlerCmd.Flags().Lookup("http-port"))
}
