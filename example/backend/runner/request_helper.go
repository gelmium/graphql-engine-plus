package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
)

type RequestOptions struct {
	SendGzip      bool
	ReceiveGzip   bool
	CustomHeaders map[string]string
}
type ResponseJson struct {
	StatusCode  int
	ResonseBody map[string]interface{}
}

func FiberJsonRequest(method string, url string, body map[string]interface{}, options RequestOptions) (ResponseJson, error) {
	// create an empty responseJson
	var responseJson ResponseJson

	jsonData, err := json.Marshal(body)
	if err != nil {
		log.Error(err)
		return responseJson, err
	}

	// create fiber request agent
	var agent *fiber.Agent
	switch method {
	case "POST":
		agent = fiber.Post(url)
	case "GET":
		agent = fiber.Get(url)
	case "PUT":
		agent = fiber.Put(url)
	case "DELETE":
		agent = fiber.Delete(url)
	case "PATCH":
		agent = fiber.Patch(url)
	case "HEAD":
		agent = fiber.Head(url)
	default:
		if body != nil {
			// with request body default to POST
			agent = fiber.Post(url)
		} else {
			// without request body default to GET
			agent = fiber.Get(url)
		}
	}
	// add custom headers
	if options.CustomHeaders != nil {
		for key, value := range options.CustomHeaders {
			agent.Add(key, value)
		}
	}
	// add content type json header
	agent.Add("Content-Type", "application/json")

	if options.SendGzip {
		// add gzip header
		agent.Add("Content-Encoding", "gzip")
		// zip the payload with archive/zip to compress the data
		// this is to bypass the 6MB payload limitation of lambda
		var buf_body bytes.Buffer
		gz_writer := gzip.NewWriter(&buf_body)
		gz_writer.Write(jsonData)
		gz_writer.Close()
		agent.Body(buf_body.Bytes())
	} else {
		// standard json request
		agent.Body(jsonData)
	}

	if options.ReceiveGzip {
		// add header to accept response in gzip format
		agent.Add("Accept-Encoding", "gzip")
	}

	// send request to server
	if err := agent.Parse(); err != nil {
		log.Error(err)
		return responseJson, err
	}
	// read raw response from server
	code, bytes_response, errs := agent.Bytes()
	if len(errs) > 0 {
		log.Error(errs)
		return responseJson, errs[0]
	}
	responseJson.StatusCode = code
	if code >= 400 {
		log.Error("Non 2xx Error: ", string(bytes_response))
		return responseJson, errors.New("Non 2xx Error when calling lambda function")
	}

	// decode the uncompressed response to json
	var responseJsonBody map[string]interface{}

	if options.ReceiveGzip {
		// read the response, gzip uncompress it using a gzip.Reader
		gz_reader, err := gzip.NewReader(bytes.NewReader(bytes_response))
		if err != nil {
			log.Error(err)
			return responseJson, err
		}
		// dont close gz_reader now be cause we need to read it
		// close the gz_reader when done
		defer gz_reader.Close()

		// read directly from gz_reader and decode json to responseJson map
		err = json.NewDecoder(gz_reader).Decode(&responseJsonBody)
		if err != nil {
			log.Error(err)
			return responseJson, err
		}
	} else {
		//  decode bytes_response json to responseJson map
		err = json.Unmarshal(bytes_response, &responseJsonBody)
		if err != nil {
			log.Error(err)
			return responseJson, err
		}
	}
	responseJson.ResonseBody = responseJsonBody
	return responseJson, nil
}
