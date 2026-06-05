package app

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

const (
	InvoiceUnpaid    = "UNPAID"
	InvoicePartial   = "PARTIAL"
	InvoicePaid      = "PAID"
	InvoiceOverpaid  = "OVERPAID"
	InvoiceCancelled = "CANCELLED"
	InvoiceRefunded  = "REFUNDED"
	InvoiceOutgoing  = "OUTGOING"

	PayoutInProgress = "IN_PROGRESS"
	PayoutSuccess    = "SUCCESS"
	PayoutFail       = "FAIL"
)

type User struct {
	ID            int64
	Username      string
	Passhash      sql.NullString
	APIKey        sql.NullString
	TOTPSecret    sql.NullString
	TOTPEnabled   bool
	BackupCodes   sql.NullString
	TOTPEnabledAt sql.NullTime
}

type Wallet struct {
	ID            int64
	Crypto        string
	ServerKey     sql.NullString
	PDest         sql.NullString
	PFee          sql.NullString
	Payout        bool
	PPolicy       string
	PCond         sql.NullString
	LastAttempt   sql.NullTime
	Enabled       bool
	APIKey        sql.NullString
	LLimit        decimal.Decimal
	ULimit        decimal.Decimal
	Recalc        int
	Confirmations int
	BKey          sql.NullString
	PresPolicy    string
	PresAmount    sql.NullString
}

type WalletAutopayout struct {
	PDest         sql.NullString
	PFee          sql.NullString
	Payout        bool
	PPolicy       string
	PCond         sql.NullString
	LLimit        decimal.Decimal
	ULimit        decimal.Decimal
	Recalc        int
	Confirmations int
	PresPolicy    string
	PresAmount    sql.NullString
}

type ExchangeRate struct {
	ID        int64           `json:"id"`
	Source    string          `json:"source"`
	Crypto    string          `json:"crypto"`
	Fiat      string          `json:"fiat"`
	Rate      decimal.Decimal `json:"rate"`
	Fee       decimal.Decimal `json:"fee"`
	FixedFee  decimal.Decimal `json:"fixed_fee"`
	FeePolicy string          `json:"fee_policy"`
}

type PayoutDestination struct {
	ID      int64
	Crypto  string
	Addr    string
	Comment string
}

type Invoice struct {
	ID            int64
	Crypto        string
	Addr          string
	ExternalID    string
	Fiat          string
	CallbackURL   string
	BalanceFiat   decimal.Decimal
	BalanceCrypto decimal.Decimal
	AmountFiat    decimal.Decimal
	AmountCrypto  decimal.Decimal
	ExchangeRate  decimal.Decimal
	Status        string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type InvoiceAddress struct {
	ID        int64
	InvoiceID int64
	Crypto    string
	Addr      string
	CreatedAt time.Time
}

type Transaction struct {
	ID                    int64
	InvoiceID             int64
	TxID                  string
	Crypto                string
	AmountCrypto          decimal.Decimal
	AmountFiat            decimal.Decimal
	NeedMoreConfirmations bool
	CallbackConfirmed     bool
	CreatedAt             time.Time
	UpdatedAt             time.Time
	Addr                  string
	Invoice               *Invoice
}

type UnconfirmedTransaction struct {
	ID                int64
	InvoiceID         int64
	Addr              string
	TxID              string
	Crypto            string
	AmountCrypto      decimal.Decimal
	CallbackConfirmed bool
	CreatedAt         time.Time
}

type Payout struct {
	ID           int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Amount       decimal.Decimal
	Crypto       string
	DestAddr     string
	Success      sql.NullString
	Error        sql.NullString
	CallbackURL  sql.NullString
	TaskID       sql.NullString
	ExternalID   sql.NullString
	Status       string
	Transactions []PayoutTx
}

type PayoutTx struct {
	ID        int64
	PayoutID  int64
	CreatedAt time.Time
	UpdatedAt time.Time
	TxID      string
	Status    string
}

type Notification struct {
	ID                int64
	TxID              sql.NullString
	Crypto            sql.NullString
	AmountCrypto      decimal.Decimal
	CallbackConfirmed bool
	Type              string
	Retries           int
	ObjectID          int64
	CallbackURL       string
	Message           sql.NullString
	CreatedAt         time.Time
}

type OrderRecord struct {
	ExternalID string          `json:"external_id"`
	Invoices   []InvoiceDetail `json:"invoices"`
	Payouts    []Payout        `json:"payouts"`
}

type OrderListPage struct {
	Orders     []OrderRecord `json:"orders"`
	NextCursor string        `json:"next_cursor"`
}

type OrderCursorRow struct {
	ExternalID string
	SortAt     time.Time
}

type InvoiceDetail struct {
	Invoice        Invoice                  `json:"invoice"`
	Addresses      []InvoiceAddress         `json:"addresses"`
	Transactions   []Transaction            `json:"transactions"`
	UnconfirmedTXs []UnconfirmedTransaction `json:"unconfirmed_transactions"`
}

type OrderFilter struct {
	ExternalID string
	Status     string
	Crypto     string
	FromDate   string
	ToDate     string
	Limit      int
	Offset     int
	Cursor     string
}

func nullStringValue(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}
