package mas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrNoCart means no cart id is stored yet: `mas cart get` says so instead of
// creating a cart behind the user's back.
var ErrNoCart = errors.New("no cart yet")

// ErrCartNotFound means the store no longer knows the stored id: guest carts expire.
var ErrCartNotFound = errors.New("cart not found")

// cartFields is the selection shared by every cart answer. itemsV2 is the only item
// list the schema has: Cart has no `items` field.
const cartFields = `id total_quantity itemsV2{total_count items{uid quantity product{sku name} prices{price{value currency} row_total{value currency}}}} prices{grand_total{value currency}}`

const (
	createCartMutation = `mutation{createEmptyCart}`
	cartQuery          = `query($c:String!){cart(cart_id:$c){` + cartFields + `}}`
	addItemMutation    = `mutation($c:String!,$i:[CartItemInput!]!){addProductsToCart(cartId:$c,cartItems:$i){cart{` + cartFields + `} user_errors{code message}}}`
	removeItemMutation = `mutation($c:String!,$u:ID!){removeItemFromCart(input:{cart_id:$c,cart_item_uid:$u}){cart{` + cartFields + `}}`
)

// Cart is the stable JSON document `mas cart get --json` prints:
//
//	{"id":"<opaque cart id>","currency":"EUR","total":5.78,
//	 "lines":[{"sku":"114042","name":"Leche condensada LA LECHERA 370 gr",
//	           "quantity":2,"unitPrice":2.89,"lineTotal":5.78}]}
//
// unitPrice, lineTotal and total are omitted when MAS publishes no price, so a missing
// key never reads as 0,00 € (S8). lines is always present, as [] for an empty cart.
type Cart struct {
	ID       string   `json:"id"`
	Currency string   `json:"currency,omitempty"`
	Total    *float64 `json:"total,omitempty"`
	Lines    []Line   `json:"lines"`
}

// Line is one cart line: what the store publishes for that sku, never derived.
type Line struct {
	SKU       string   `json:"sku"`
	Name      string   `json:"name"`
	Quantity  float64  `json:"quantity"`
	UnitPrice *float64 `json:"unitPrice,omitempty"`
	LineTotal *float64 `json:"lineTotal,omitempty"`

	// uid is the cart item id removeItemFromCart wants; it is not printed.
	uid string
}

type gqlMoney struct {
	Value    *float64 `json:"value"`
	Currency string   `json:"currency"`
}

type gqlCart struct {
	ID            string `json:"id"`
	TotalQuantity int    `json:"total_quantity"`
	ItemsV2       struct {
		TotalCount int           `json:"total_count"`
		Items      []gqlCartItem `json:"items"`
	} `json:"itemsV2"`
	Prices struct {
		GrandTotal gqlMoney `json:"grand_total"`
	} `json:"prices"`
}

type gqlCartItem struct {
	UID      string  `json:"uid"`
	Quantity float64 `json:"quantity"`
	Product  struct {
		SKU  string `json:"sku"`
		Name string `json:"name"`
	} `json:"product"`
	Prices struct {
		Price    gqlMoney `json:"price"`
		RowTotal gqlMoney `json:"row_total"`
	} `json:"prices"`
}

// cart converts the GraphQL cart into the document the CLI prints.
func (g gqlCart) cart() Cart {
	c := Cart{ID: g.ID, Currency: g.Prices.GrandTotal.Currency, Total: g.Prices.GrandTotal.Value, Lines: []Line{}}
	for _, it := range g.ItemsV2.Items {
		if c.Currency == "" {
			c.Currency = it.Prices.Price.Currency
		}
		c.Lines = append(c.Lines, Line{
			SKU:       it.Product.SKU,
			Name:      it.Product.Name,
			Quantity:  it.Quantity,
			UnitPrice: it.Prices.Price.Value,
			LineTotal: it.Prices.RowTotal.Value,
			uid:       it.UID,
		})
	}
	return c
}

// CreateCart asks the store for a new anonymous guest cart and returns its opaque id.
func (c *Client) CreateCart(ctx context.Context) (string, error) {
	var data struct {
		CreateEmptyCart string `json:"createEmptyCart"`
	}
	if err := c.query(ctx, createCartMutation, nil, &data); err != nil {
		return "", err
	}
	if data.CreateEmptyCart == "" {
		return "", errors.New("createEmptyCart returned no cart id")
	}
	return data.CreateEmptyCart, nil
}

// AddItem puts one sku in the cart. Magento reports a rejected add inside
// data.user_errors, not as an HTTP error, so those become a Go error here.
func (c *Client) AddItem(ctx context.Context, cartID, sku string, qty int) (Cart, error) {
	var data struct {
		AddProductsToCart struct {
			Cart       gqlCart    `json:"cart"`
			UserErrors []gqlError `json:"user_errors"`
		} `json:"addProductsToCart"`
	}
	vars := map[string]any{"c": cartID, "i": []map[string]any{{"sku": sku, "quantity": qty}}}
	if err := c.query(ctx, addItemMutation, vars, &data); err != nil {
		return Cart{}, err
	}
	if ue := data.AddProductsToCart.UserErrors; len(ue) > 0 {
		return Cart{}, fmt.Errorf("add %s: %s: %s", sku, ue[0].Code, ue[0].Message)
	}
	return data.AddProductsToCart.Cart.cart(), nil
}

// Cart reads the cart back. An id the store does not know returns ErrCartNotFound.
func (c *Client) Cart(ctx context.Context, cartID string) (Cart, error) {
	var data struct {
		Cart gqlCart `json:"cart"`
	}
	if err := c.query(ctx, cartQuery, map[string]any{"c": cartID}, &data); err != nil {
		return Cart{}, err
	}
	if data.Cart.ID == "" {
		return Cart{}, errors.New("the cart query returned no cart")
	}
	return data.Cart.cart(), nil
}

// RemoveItem drops the cart line for sku. There is no by-sku mutation, so the cart is
// read first to resolve the sku to its cart item uid.
func (c *Client) RemoveItem(ctx context.Context, cartID, sku string) (Cart, error) {
	current, err := c.Cart(ctx, cartID)
	if err != nil {
		return Cart{}, err
	}
	uid, ok := current.uidOf(sku)
	if !ok {
		return Cart{}, fmt.Errorf("sku %s is not in cart %s", sku, cartID)
	}
	var data struct {
		RemoveItemFromCart struct {
			Cart gqlCart `json:"cart"`
		} `json:"removeItemFromCart"`
	}
	if err := c.query(ctx, removeItemMutation, map[string]any{"c": cartID, "u": uid}, &data); err != nil {
		return Cart{}, err
	}
	return data.RemoveItemFromCart.Cart.cart(), nil
}

// uidOf finds the cart item uid of a sku.
func (c Cart) uidOf(sku string) (string, bool) {
	for _, l := range c.Lines {
		if l.SKU == sku {
			return l.uid, true
		}
	}
	return "", false
}

// query posts one GraphQL document and unmarshals its `data` into out.
func (c *Client) query(ctx context.Context, query string, vars map[string]any, out any) error {
	payload := map[string]any{"query": query}
	if vars != nil {
		payload["variables"] = vars
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding query: %w", err)
	}
	raw, err := c.post(ctx, body)
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("invalid GraphQL response: %w", err)
	}
	if len(env.Errors) > 0 {
		msg := env.Errors[0].Message
		if strings.Contains(strings.ToLower(msg), "no se pudo encontrar un carrito") {
			return fmt.Errorf("%w: %s", ErrCartNotFound, msg)
		}
		return fmt.Errorf("graphql: %s", msg)
	}
	if len(env.Data) == 0 {
		return errors.New("GraphQL response carries no data")
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("invalid GraphQL data: %w", err)
	}
	return nil
}

type gqlError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}
