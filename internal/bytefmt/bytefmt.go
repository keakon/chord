package bytefmt

import "fmt"

var shortUnits = []string{"B", "KB", "MB", "GB", "TB", "PB", "EB"}

// Short formats bytes using binary scaling with short unit labels, for example
// "847 B", "263.5 MB", or "29.6 GB".
func Short(n int64) string {
	return formatBinary(n, shortUnits, fixedOneDecimal)
}

// Compact formats bytes using the same short binary unit labels as Short, but
// with width-sensitive precision suitable for tight UI such as the status bar.
func Compact(n int64) string {
	return formatBinary(n, shortUnits, compactScaled)
}

func formatBinary(n int64, units []string, scaled func(float64, string) string) string {
	if n <= 0 {
		return "0 B"
	}
	value := float64(n)
	unitIndex := 0
	for value >= 1024 && unitIndex < len(units)-1 {
		value /= 1024
		unitIndex++
	}
	if unitIndex == 0 {
		return fmt.Sprintf("%d %s", n, units[unitIndex])
	}
	return scaled(value, units[unitIndex])
}

func fixedOneDecimal(value float64, unit string) string {
	return fmt.Sprintf("%.1f %s", value, unit)
}

func compactScaled(value float64, unit string) string {
	if value >= 10 {
		return fmt.Sprintf("%.0f %s", value, unit)
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}
