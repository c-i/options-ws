package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"nhooyr.io/websocket"
)

const AevoHttp string = "https://api.aevo.xyz"
const AevoWss string = "wss://ws.aevo.xyz"

type wssData struct {
	Op   string   `json:"op"`
	Data []string `json:"data"`
}

type Greeks struct {
	Delta float64 `json:"delta,string"`
	Theta float64 `json:"theta,string"`
	Gamma float64 `json:"gamma,string"`
	Rho   float64 `json:"rho,string"`
	Vega  float64 `json:"vega,string"`
	Iv    float64 `json:"iv,string"`
}

type Market struct {
	InstrumentId     int64   `json:"instrument_id,string"`
	InstrumentName   string  `json:"instrument_name"`
	InstrumentType   string  `json:"instrument_type"`
	UnderlyingAsset  string  `json:"underlying_asset"`
	QuoteAsset       string  `json:"quote_asset"`
	PriceStep        float64 `json:"price_step,string"`
	AmountStep       float64 `json:"amount_step,string"`
	MinOrderValue    float64 `json:"min_order_value,string"`
	MaxOrderValue    float64 `json:"max_order_value,string"`
	MaxNotionalValue float64 `json:"max_notional_value,string"`
	MarkPrice        float64 `json:"mark_price,string"`
	ForwardPrice     float64 `json:"forward_price,string"`
	IndexPrice       float64 `json:"index_price,string"`
	IsActive         bool    `json:"is_active"`
	OptionType       string  `json:"option_type"`
	Expiry           int64   `json:"expiry,string"`
	Strike           int64   `json:"strike,string"`
	Greeks           Greeks  `json:"greeks"`
}

type Order struct {
	Price  float64
	Amount float64
	Iv     float64
}

type OrderbookData struct {
	Bids        []Order
	Asks        []Order
	LastUpdated int64
}

type ArbTable struct {
	Bid       Order
	Ask       Order
	BidType   string
	AskType   string
	AbsProfit float64
	RelProfit float64
	Apy       float64
}

var Orderbooks map[string]*OrderbookData = make(map[string]*OrderbookData) //pointer seems like a bad idea but makes assignment of elements easier
var ArbTables map[string]*ArbTable = make(map[string]*ArbTable)
var Index map[string]float64 = make(map[string]float64)

func markets(asset string) []Market {
	url := AevoHttp + "/markets?asset=" + asset + "&instrument_type=OPTION"

	req, _ := http.NewRequest("GET", url, nil) //NewRequest + Client.Do used to pass headers, otherwise http.Get can be used

	req.Header.Add("accept", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("markets request error: %v", err)
	}

	defer res.Body.Close() //Client.Do, http.Get, http.Post, etc all need response Body to be closed when done reading from it
	// defer defers execution until enclosing function returns

	var markets []Market

	decoder := json.NewDecoder(res.Body)
	err = decoder.Decode(&markets)
	if err != nil {
		log.Fatalf("markets json decode error: %v", err)
	}

	return markets
}

func instruments(markets []Market) []string {
	var instruments []string
	for _, market := range markets {
		if market.IsActive {
			instruments = append(instruments, market.InstrumentName)
		}
	}

	return instruments
}

func orderbookJson(instruments []string) []byte {
	var orderbooks []string
	for _, instrument := range instruments {
		orderbooks = append(orderbooks, "orderbook:"+instrument)
	}

	data := wssData{
		Op:   "subscribe",
		Data: orderbooks,
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Fatalf("orderbook json marshal error: %v", err)
	}

	return jsonData
}

func indexJson(assets []string) []byte {
	var indices []string
	for _, asset := range assets {
		indices = append(indices, "index:"+asset)
	}

	data := wssData{
		Op:   "subscribe",
		Data: indices,
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Fatalf("orderbook json marshal error: %v", err)
	}

	return jsonData
}

func wssReqOrderbook(instruments []string, ctx context.Context, c *websocket.Conn) {
	var data []byte
	for i := 0; true; i += 20 {
		if i+20 < len(instruments) {
			data = orderbookJson(instruments[i : i+20])
		} else {
			data = orderbookJson(instruments[i:])
		}

		fmt.Printf("subscribe: %v\n\n", string(data))
		err := c.Write(ctx, 1, data)
		if err != nil {
			log.Fatalf("Write error: %v\n", err)
		}

		if i+20 > len(instruments) {
			break
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func wssReqIndex(assets []string, ctx context.Context, c *websocket.Conn) {
	data := indexJson(assets)
	fmt.Printf("subscribe: %v\n\n", string(data))

	err := c.Write(ctx, 1, data)
	if err != nil {
		log.Fatalf("Write error: %v\n", err)
	}
}

func unpackOrders(orders []interface{}) ([]Order, error) {
	unpackedOrders := make([]Order, 0)
	for _, order := range orders {
		orderArr, ok := order.([]interface{})

		if !ok {
			return unpackedOrders, errors.New("orders not of []interface{} type")
		}
		if len(orderArr) != 3 {
			return unpackedOrders, errors.New("orders not length 3")
		}

		priceStr, priceOk := orderArr[0].(string)
		amountStr, amountOk := orderArr[1].(string)
		ivStr, ivOk := orderArr[2].(string)
		if !priceOk || !amountOk || !ivOk {
			return unpackedOrders, errors.New("unable to convert interface{} element to string")
		}

		price, priceErr := strconv.ParseFloat(priceStr, 64)
		amount, amountErr := strconv.ParseFloat(amountStr, 64)
		iv, ivErr := strconv.ParseFloat(ivStr, 64)
		if priceErr != nil || amountErr != nil || ivErr != nil {
			log.Printf("%v\n", priceErr)
			log.Printf("%v\n", amountErr)
			log.Printf("%v\n", ivErr)
			return unpackedOrders, errors.New("error converting string to float64")
		}

		unpackedOrders = append(unpackedOrders, Order{price, amount, iv})
	}

	return unpackedOrders, nil
}

func updateOrderbooks(res map[string]interface{}) {
	data, ok := res["data"].(map[string]interface{})
	if !ok {
		log.Printf("updateOrderbooks: unable to cast response to type map[string]interface{}\n")
		return
	}

	instrument, ok := data["instrument_name"].(string)
	bidsRaw, bidsOk := data["bids"].([]interface{})
	asksRaw, asksOk := data["asks"].([]interface{})
	timeStr, timeOk := data["last_updated"].(string)
	if (!ok || !timeOk) || !(bidsOk || asksOk) {
		log.Printf("updateOrderbooks: unable to convert field")
		return
	}

	bids, bidsErr := unpackOrders(bidsRaw)
	asks, asksErr := unpackOrders(asksRaw)
	if bidsErr != nil && asksErr != nil {
		log.Printf("unpackOrders error: \n%v\n", bidsErr)
		log.Printf("%v\n", asksErr)
		return
	}

	lastUpdated, err := strconv.ParseInt(timeStr, 10, 64)
	if err != nil {
		log.Printf("Failed to convert last_updated timestamp to int64: %v\n", err)
		return
	}

	Orderbooks[instrument] = &OrderbookData{
		Bids:        bids,
		Asks:        asks,
		LastUpdated: lastUpdated,
	}

	fmt.Printf("%v: %+v\n\n", instrument, Orderbooks[instrument])
}

func updateIndex(res map[string]interface{}) {
	channel, ok := res["channel"].(string)
	if !ok {
		log.Printf("updateIndex: unable to convert response 'channel' to string\n\n")
		return
	}

	data, ok := res["data"].(map[string]interface{})
	if !ok {
		log.Printf("updateIndex: unable to cast response to type map[string]interface{}\n\n")
		return
	}

	asset := strings.TrimPrefix(channel, "index:")
	// fmt.Printf("asset: %v\n\n", asset)

	priceStr, ok := data["price"].(string)
	if !ok {
		log.Printf("updateIndex: unable to cast field to type string: %v\n\n", reflect.TypeOf(priceStr))
		return
	}

	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		log.Printf("updateIndex: error converting string to float64: %v\n\n", err)
		return
	}

	Index[asset] = price
	fmt.Printf("index: %+v\n\n", Index)
// }

// func updateArbTables() {
// 	for key, value := range Orderbooks {
// 		components := strings.Split(key, "-")
// 		expiry := components[1]
// 		strike := components[2]
// 		optionType := components[3]
// 		// for key2, value2 := range Orderbooks{

// 		// }
// 	}
// }

func wssRead(ctx context.Context, c *websocket.Conn) []byte {
	_, raw, err := c.Read(ctx)
	if err != nil {
		log.Fatalf("Read error: %v", err)
	}

	return raw //return error as well?
}

func wssReadLoop(ctx context.Context, c *websocket.Conn) { //add exit condition, add ping or use Reader instead of Read to automatically manage ping, disconnect, etc
	var raw []byte
	var res map[string]interface{}
	for {
		raw = wssRead(ctx, c)
		err := json.Unmarshal(raw, &res)
		if err != nil {
			log.Printf("readLoop: error unmarshaling orderbookRaw: %v\n\n", err)
			continue
		}

		channel, ok := res["channel"].(string)
		if !ok {
			log.Printf("readLoop: unable to convert response 'channel' to string\n\n")
			continue
		}

		if strings.Contains(channel, "orderbook") {
			updateOrderbooks(res)
		}

		if strings.Contains(channel, "index") {
			// fmt.Printf("index: %v\n\n", string(raw))
			updateIndex(res)
		}

	}
}

func main() {
	assets := []string{"ETH"}
	markets := markets("ETH")
	instruments := instruments(markets)
	fmt.Printf("Number of instruments: %v\n\n", len(instruments))
	// fmt.Printf("%+v", Orderbooks)
	// fmt.Printf("%v\n", instruments)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute*20)
	defer cancel()

	c, res, err := websocket.Dial(ctx, AevoWss, nil)
	if err != nil {
		log.Fatalf("Dial error: %v", err)
	}
	fmt.Printf("%v\n\n", res)
	defer c.Close(websocket.StatusNormalClosure, "")
	defer c.CloseNow()

	wssReqOrderbook(instruments, ctx, c)
	wssReqIndex(assets, ctx, c)
	wssReadLoop(ctx, c)
}

/*
	websocket read loop goroutine

	calculation goroutine: arbEngine: calculate put call parity opportunities and update table

	UI goroutine: update htmx frontend
*/
