// Copyright (c) OpenFaaS Author(s). All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

package handlers

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

// ScalingConfig for scaling behaviours
type ScalingConfig struct {
	// MaxPollCount attempts to query a function before giving up
	MaxPollCount uint

	// FunctionPollInterval delay or interval between polling a function's readiness status
	FunctionPollInterval time.Duration

	// CacheExpiry life-time for a cache entry before considering invalid
	CacheExpiry time.Duration

	// ServiceQuery queries available/ready replicas for function
	ServiceQuery ServiceQuery
}

// MakeScalingHandler creates handler which can scale a function from
// zero to N replica(s). After scaling the next http.HandlerFunc will
// be called. If the function is not ready after the configured
// amount of attempts / queries then next will not be invoked and a status
// will be returned to the client.
func MakeScalingHandler(next http.HandlerFunc, config ScalingConfig) http.HandlerFunc {
	cache := FunctionCache{
		Cache:  make(map[string]*FunctionMeta),
		Expiry: config.CacheExpiry,
	}

	return func(w http.ResponseWriter, r *http.Request) {

		functionName := getServiceName(r.URL.String())

		if serviceQueryResponse, hit := cache.Get(functionName); hit && serviceQueryResponse.AvailableReplicas > 0 {
			next.ServeHTTP(w, r)
			return
		}

		queryResponse, err := config.ServiceQuery.GetReplicas(functionName)

		if err != nil {
			var errStr string
			errStr = fmt.Sprintf("error finding function %s: %s", functionName, err.Error())

			log.Printf(errStr)
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(errStr))
			return
		}

		cache.Set(functionName, queryResponse)

		if queryResponse.AvailableReplicas == 0 {
			minReplicas := uint64(1)
			if queryResponse.MinReplicas > 0 {
				minReplicas = queryResponse.MinReplicas
			}

			log.Printf("[Scale] function=%s 0 => %d requested", functionName, minReplicas)
			scalingStartTime := time.Now()

			err := config.ServiceQuery.SetReplicas(functionName, minReplicas)
			if err != nil {
				errStr := fmt.Errorf("unable to scale function [%s], err: %s", functionName, err)
				log.Printf(errStr.Error())

				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(errStr.Error()))
				return
			}

			for i := 0; i < int(config.MaxPollCount); i++ {
				queryResponse, err := config.ServiceQuery.GetReplicas(functionName)
				cache.Set(functionName, queryResponse)

				if err != nil {
					errStr := fmt.Sprintf("error: %s", err.Error())
					log.Printf(errStr)

					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(errStr))
					return
				}

				if queryResponse.AvailableReplicas > 0 {
					scalingDuration := time.Since(scalingStartTime)
					log.Printf("[Scale] function=%s 0 => %d successful - %f seconds", functionName, queryResponse.AvailableReplicas, scalingDuration.Seconds())
					break
				}

				time.Sleep(config.FunctionPollInterval)
			}
		}

		next.ServeHTTP(w, r)
	}
}
