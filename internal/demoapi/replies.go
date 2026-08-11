package demoapi

// replies is what each method answers with, as protojson keyed by the method's
// fully-qualified name. A server-streaming method sends every entry of its
// slice in order, one per [streamGap]; a unary one sends the first; a bidi one
// wraps round for as long as the client keeps talking.
//
// `@now` and `@recent` are replaced with timestamps when the reply is built —
// see [expand]. Everything else is literal, and deliberately plausible: the
// point of a demo is that the reader recognises the shape of what they are
// looking at.
// endless names the server-streaming methods that never finish on their own.
// A list ends when the list does; a watch ends when the client stops watching,
// and a demo where it ended by itself after four messages would be showing
// something grpctui does not have to handle.
var endless = map[string]bool{
	"demo.v1.OrderService.WatchOrders": true,
}

var replies = map[string][]string{
	"demo.v1.UserService.GetUser": {`{
		"id": "usr_8f21c4",
		"email": "ada@example.com",
		"displayName": "Ada Lovelace",
		"role": "ROLE_ADMIN",
		"teams": ["platform", "on-call"],
		"address": {
			"line1": "12 Analytical Way",
			"city": "London",
			"country": "GB",
			"postcode": "EC1A 4TP"
		},
		"labels": {"tier": "internal", "region": "eu-west-1"},
		"createdAt": "@recent"
	}`},

	"demo.v1.UserService.CreateUser": {`{
		"id": "usr_new001",
		"email": "grace@example.com",
		"displayName": "Grace Hopper",
		"role": "ROLE_MEMBER",
		"teams": ["compilers"],
		"address": {
			"line1": "1 Nanosecond Row",
			"city": "New York",
			"country": "US",
			"postcode": "10004"
		},
		"labels": {"tier": "internal"},
		"createdAt": "@now"
	}`},

	"demo.v1.UserService.ListUsers": {
		`{"id": "usr_8f21c4", "email": "ada@example.com", "displayName": "Ada Lovelace", "role": "ROLE_ADMIN", "teams": ["platform"], "createdAt": "@recent"}`,
		`{"id": "usr_1b90de", "email": "alan@example.com", "displayName": "Alan Turing", "role": "ROLE_MEMBER", "teams": ["research"], "createdAt": "@recent"}`,
		`{"id": "usr_c40a77", "email": "grace@example.com", "displayName": "Grace Hopper", "role": "ROLE_MEMBER", "teams": ["compilers"], "createdAt": "@now"}`,
	},

	"demo.v1.OrderService.GetOrder": {`{
		"id": "ord_5512",
		"userId": "usr_8f21c4",
		"state": "ORDER_STATE_SHIPPED",
		"items": [
			{"sku": "KBD-60-BROWN", "quantity": 1, "unitPriceCents": "12900"},
			{"sku": "CBL-USBC-2M", "quantity": 2, "unitPriceCents": "1450"}
		],
		"totalCents": "15800",
		"placedAt": "@recent"
	}`},

	"demo.v1.OrderService.WatchOrders": {
		`{"order": {"id": "ord_5512", "userId": "usr_8f21c4", "state": "ORDER_STATE_PICKED", "items": [{"sku": "KBD-60-BROWN", "quantity": 1, "unitPriceCents": "12900"}], "totalCents": "15800", "placedAt": "@recent"}, "from": "ORDER_STATE_PLACED", "to": "ORDER_STATE_PICKED", "at": "@now"}`,
		`{"order": {"id": "ord_5513", "userId": "usr_1b90de", "state": "ORDER_STATE_PLACED", "items": [{"sku": "MSE-ERGO-L", "quantity": 1, "unitPriceCents": "4200"}], "totalCents": "4200", "placedAt": "@now"}, "from": "ORDER_STATE_UNSPECIFIED", "to": "ORDER_STATE_PLACED", "at": "@now"}`,
		`{"order": {"id": "ord_5512", "userId": "usr_8f21c4", "state": "ORDER_STATE_SHIPPED", "items": [{"sku": "KBD-60-BROWN", "quantity": 1, "unitPriceCents": "12900"}], "totalCents": "15800", "placedAt": "@recent"}, "from": "ORDER_STATE_PICKED", "to": "ORDER_STATE_SHIPPED", "at": "@now"}`,
		`{"order": {"id": "ord_5511", "userId": "usr_c40a77", "state": "ORDER_STATE_DELIVERED", "items": [{"sku": "DSK-MAT-XL", "quantity": 3, "unitPriceCents": "3300"}], "totalCents": "9900", "placedAt": "@recent"}, "from": "ORDER_STATE_SHIPPED", "to": "ORDER_STATE_DELIVERED", "at": "@now"}`,
	},

	// accepted is overwritten with the number of messages that actually
	// arrived; see [collect].
	"demo.v1.OrderService.ImportOrders": {`{"accepted": 0, "rejected": 0, "errors": []}`},

	"demo.v1.OrderService.TrackShipment": {
		`{"shipmentId": "shp_77", "status": "in transit", "minutesOut": 42}`,
		`{"shipmentId": "shp_77", "status": "out for delivery", "minutesOut": 12}`,
		`{"shipmentId": "shp_77", "status": "delivered", "minutesOut": 0}`,
	},

	"demo.v1.InventoryService.GetStock": {`{
		"sku": "KBD-60-BROWN",
		"onHand": 34,
		"reserved": 6,
		"backordered": false
	}`},
}
