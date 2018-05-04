package api

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"strconv"
	"time"

	"github.com/battlesnakeio/engine/controller/pb"
	"github.com/battlesnakeio/engine/rules"
	"github.com/gorilla/websocket"
	"github.com/julienschmidt/httprouter"
	"github.com/rs/cors"
	log "github.com/sirupsen/logrus"
)

// Server this is the api server
type Server struct {
	hs *http.Server
}

// clientHandle is a function that handles an http route and accepts a ControllerClient
// in addition to the normal httprouter.Handle parameters.
type clientHandle func(http.ResponseWriter, *http.Request, httprouter.Params, pb.ControllerClient)

// New creates a new api server
func New(addr string, c pb.ControllerClient) *Server {
	router := httprouter.New()
	router.POST("/games", newClientHandle(c, createGame))
	router.POST("/games/:id/start", newClientHandle(c, startGame))
	router.GET("/games/:id", newClientHandle(c, getStatus))
	router.GET("/games/:id/frames", newClientHandle(c, getFrames))
	router.GET("/socket/:id", newClientHandle(c, framesSocket))

	handler := cors.Default().Handler(router)

	return &Server{
		hs: &http.Server{
			Addr:    addr,
			Handler: handler,
		},
	}
}

func newClientHandle(c pb.ControllerClient, innerHandle clientHandle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
		innerHandle(w, r, p, c)
	}
}

var upgrader = websocket.Upgrader{}

func framesSocket(w http.ResponseWriter, r *http.Request, ps httprouter.Params, c pb.ControllerClient) {
	id := ps.ByName("id")
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Error("Unable to upgrade connection")
		return
	}
	defer ws.Close()
	frames := make(chan *pb.GameFrame)
	go gatherFrames(frames, c, id)
	for frame := range frames {
		data, err := json.Marshal(frame)
		if err != nil {
			log.WithError(err).Error("Unable to serialize frame for websocket")
		}
		err = ws.WriteMessage(websocket.TextMessage, data)
		if err != nil {
			log.WithError(err).Error("Unable to write to websocket")
			break
		}
	}
}

func gatherFrames(frames chan<- *pb.GameFrame, c pb.ControllerClient, id string) {
	offset := int32(0)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := c.ListGameFrames(ctx, &pb.ListGameFramesRequest{
			ID:     id,
			Offset: offset,
		})
		if err != nil {
			log.WithError(err).Error("Error while retrieving frames from controller")
			cancel()
			break
		}

		for _, f := range resp.Frames {
			frames <- f
		}

		offset += int32(len(resp.Frames))

		if len(resp.Frames) == 0 {
			sg, err := c.Status(ctx, &pb.StatusRequest{
				ID: id,
			})
			if err != nil {
				log.WithError(err).Error("Error while retrieving game status from controller")
				cancel()
				break
			}

			if sg.Game.Status != rules.GameStatusRunning {
				cancel()
				break
			}
		}

		cancel()
	}
	close(frames)
}

func createGame(w http.ResponseWriter, r *http.Request, _ httprouter.Params, c pb.ControllerClient) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		log.WithError(err).Error("Unable to read request body")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	req := &pb.CreateRequest{}

	err = json.Unmarshal(body, req)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("Invalid JSON: " + err.Error()))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.Create(ctx, req)
	if err != nil {
		log.WithError(err).Error("Error creating game")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	j, err := json.Marshal(resp)
	if err != nil {
		log.WithError(err).WithField("resp", resp).Error("Error serializing to JSON")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(j)
}

func startGame(w http.ResponseWriter, r *http.Request, ps httprouter.Params, c pb.ControllerClient) {
	id := ps.ByName("id")
	req := &pb.StartRequest{
		ID: id,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := c.Start(ctx, req)
	if err != nil {
		log.WithError(err).WithField("req", req).Error("Error while calling controller start")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func getStatus(w http.ResponseWriter, r *http.Request, ps httprouter.Params, c pb.ControllerClient) {
	id := ps.ByName("id")
	req := &pb.StatusRequest{
		ID: id,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := c.Status(ctx, req)
	if err != nil {
		log.WithError(err).WithField("req", req).Error("Error while calling controller status")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	j, err := json.Marshal(resp)
	if err != nil {
		log.WithError(err).WithField("resp", resp).Error("Error serializing response to JSON")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(j)
}

func getFrames(w http.ResponseWriter, r *http.Request, ps httprouter.Params, c pb.ControllerClient) {
	id := ps.ByName("id")
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 0)
	limit, _ := strconv.ParseInt(r.URL.Query().Get("limit"), 10, 0)
	if limit == 0 {
		limit = 100
	}
	req := &pb.ListGameFramesRequest{
		ID:     id,
		Offset: int32(offset),
		Limit:  int32(limit),
	}
	// TODO: use a context with timeout
	resp, err := c.ListGameFrames(r.Context(), req)
	if err != nil {
		log.WithError(err).WithField("req", req).Error("Error while calling controller status")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	j, err := json.Marshal(resp)
	if err != nil {
		log.WithError(err).WithField("resp", resp).Error("Error serializing response to JSON")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(j)
}

// WaitForExit starts up the server and blocks until the server shuts down.
func (s *Server) WaitForExit() error { return s.hs.ListenAndServe() }
