package processing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	_ "image/jpeg"
	"mizuki-archive/internal/model"
)

const (
	// ThumbnailMaxWidth 和 ThumbnailMaxHeight 限制缩略图输出尺寸，避免派生文件失控增长。
	ThumbnailMaxWidth  = 640
	ThumbnailMaxHeight = 640
	// ThumbnailMaxSourcePixels 限制解码前的像素总量，防止压缩图片造成内存放大攻击。
	ThumbnailMaxSourcePixels int64 = 40_000_000
	// ThumbnailMaxSourceDimension 限制单边尺寸，避免异常图片触发过大的中间缓冲区。
	ThumbnailMaxSourceDimension = 12_000
)

var (
	// ErrInvalidImage 表示图片头信息缺失、尺寸不合理或无法解码。
	ErrInvalidImage = errors.New("invalid image")
	// ErrImageTooLarge 表示图片解码尺寸超过受控像素边界。
	ErrImageTooLarge = errors.New("image dimensions too large")
)

// GenerateThumbnail 解码受支持的图片并生成 PNG 缩略图，不修改原件也不执行任何外部内容。
func GenerateThumbnail(ctx context.Context, resource model.Resource, sourcePath string) ([]byte, error) {
	if resource.Kind != "image" {
		return nil, ErrUnsupportedType
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	file, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open image source: %w", err)
	}
	config, _, err := image.DecodeConfig(file)
	_ = file.Close()
	if err != nil {
		return nil, fmt.Errorf("read image config: %w", err)
	}
	if err := validateImageBounds(config); err != nil {
		return nil, err
	}

	file, err = os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("reopen image source: %w", err)
	}
	defer file.Close()
	source, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode image source: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	thumbnail := resizeImage(source)
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&output, thumbnail); err != nil {
		return nil, fmt.Errorf("encode thumbnail: %w", err)
	}
	return output.Bytes(), nil
}

// validateImageBounds 在真正解码前检查尺寸，避免恶意压缩图片耗尽 Worker 内存。
func validateImageBounds(config image.Config) error {
	if config.Width <= 0 || config.Height <= 0 {
		return ErrInvalidImage
	}
	if config.Width > ThumbnailMaxSourceDimension || config.Height > ThumbnailMaxSourceDimension {
		return ErrImageTooLarge
	}
	if int64(config.Width)*int64(config.Height) > ThumbnailMaxSourcePixels {
		return ErrImageTooLarge
	}
	return nil
}

// resizeImage 使用高质量缩放把长边压到受控范围，小图不放大以避免无意义的模糊。
func resizeImage(source image.Image) *image.RGBA {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	scale := 1.0
	if width > ThumbnailMaxWidth || height > ThumbnailMaxHeight {
		widthScale := float64(ThumbnailMaxWidth) / float64(width)
		heightScale := float64(ThumbnailMaxHeight) / float64(height)
		if widthScale < heightScale {
			scale = widthScale
		} else {
			scale = heightScale
		}
	}
	outputWidth := maxDimension(1, int(float64(width)*scale))
	outputHeight := maxDimension(1, int(float64(height)*scale))
	result := image.NewRGBA(image.Rect(0, 0, outputWidth, outputHeight))
	draw.CatmullRom.Scale(result, result.Bounds(), source, bounds, draw.Over, nil)
	return result
}

// maxDimension 避免极小但合法的图片因为浮点缩放变成零尺寸。
func maxDimension(minimum, value int) int {
	if value < minimum {
		return minimum
	}
	return value
}
