package tool

import (
	"fmt"
	"math"
	"strconv"
)

func FormatFloat(num float64, decimal int) string {
	// 默认乘1
	d := float64(1)
	if decimal > 0 {
		// 10的N次方
		d = math.Pow10(decimal)
	}
	// math.trunc作用就是返回浮点数的整数部分
	// 再除回去，小数点后无效的0也就不存在了
	return strconv.FormatFloat(math.Trunc(num*d)/d, 'f', -1, 64)
}

func FormatStringToFloat(str string) float64 {
	value, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return 0
	}
	return value
}

func FormatAmountFromCents(cents int64) string {
	abs := cents
	sign := ""
	if cents < 0 {
		sign = "-"
		abs = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, abs/100, abs%100)
}
