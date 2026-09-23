package handler

import (
	"net/http"

	"github.com/goxlang/gox/pkg/goweb"
)

// GetLeaderboard returns the top selling products on the real-time leaderboard.
func GetLeaderboard(c *goweb.Context) error {
	top, err := c.Redis().LeaderboardGetTop(c.Context(), "lb:product_sales", 10)
	if err != nil {
		return c.Error(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, goweb.H{
		"leaderboard": "Top Selling Products",
		"top":         top,
	})
}

// GetLeaderboardAround returns the ranking window centered on a specific product.
func GetLeaderboardAround(c *goweb.Context) error {
	id := c.Param("id")
	around, err := c.Redis().LeaderboardAroundMe(c.Context(), "lb:product_sales", id, 3)
	if err != nil {
		return c.Error(http.StatusNotFound, "product not ranked or not found on leaderboard")
	}
	return c.JSON(http.StatusOK, goweb.H{
		"product_id": id,
		"window":     around,
	})
}

// GetTrending returns heavy-hitter search queries estimated by Top-K.
func GetTrending(c *goweb.Context) error {
	items, err := c.Redis().TopKList(c.Context(), "trending:searches")
	if err != nil {
		return c.Error(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, goweb.H{
		"metric":   "trending_searches",
		"trending": items,
	})
}

// GetVisitorStats returns the estimated count of unique daily visitors via HyperLogLog.
func GetVisitorStats(c *goweb.Context) error {
	count, err := c.Redis().HyperLogLogCount(c.Context(), "hll:visitors:daily")
	if err != nil {
		return c.Error(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, goweb.H{
		"metric":          "unique_daily_visitors",
		"estimated_count": count,
	})
}
