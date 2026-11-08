// Package render draws an animated voxel tree whose shape is grown from
// GitHub stats. Everything is software-rendered pixel art, no dependencies.
package render

import (
	"hash/fnv"
	"image"
	"image/color"
	"image/gif"
	"math"
	"math/rand/v2"

	"github.com/ayaans16/harvest/internal/github"
)

const (
	W, H   = 330, 192 // low-res canvas
	Scale  = 2        // final pixels per canvas pixel
	Frames = 36
	Delay  = 6 // centiseconds
	gridN  = 37
	gridZ  = 48
	ctr    = 18 // trunk occupies ctr..ctr+1
	top    = 6  // z of the island's grass layer
)

// ---- palette ---------------------------------------------------------

type palette struct {
	list color.Palette
	idx  map[color.RGBA]uint8
}

func newPalette() *palette { return &palette{idx: map[color.RGBA]uint8{}} }

func (p *palette) add(hex uint32) uint8 {
	c := color.RGBA{uint8(hex >> 16), uint8(hex >> 8), uint8(hex), 255}
	if i, ok := p.idx[c]; ok {
		return i
	}
	i := uint8(len(p.list))
	p.list = append(p.list, c)
	p.idx[c] = i
	return i
}

func shade(hex uint32, f float64) uint32 {
	r, g, b := float64(hex>>16&255), float64(hex>>8&255), float64(hex&255)
	return uint32(r*f)<<16 | uint32(g*f)<<8 | uint32(math.Min(255, b*f*1.03+(1-f)*24))
}

type material struct{ top, left, right uint8 }

func (p *palette) mat(hex uint32) material {
	return material{p.add(hex), p.add(shade(hex, 0.84)), p.add(shade(hex, 0.68))}
}

// ---- voxel world -----------------------------------------------------

type kind uint8

const (
	solid kind = iota + 1
	leaf
	fruit
)

type voxel struct {
	set   bool
	kind  kind
	m     material
	decal uint8 // face decals on the +x face
}

const decalEye uint8 = 1

type petal struct {
	x, y  int
	phase float64
	sway  float64
	m     material
}

type butterfly struct {
	cx, cy, rx, ry int
	phase          float64
	wing           uint8
}

type scene struct {
	p   *palette
	g   [gridN][gridN][gridZ]voxel
	rng *rand.Rand
	st  github.Stats

	trunk  int // top z of trunk
	ox, oy int
	cy     int // screen y of the canopy centre

	bg                            [5]uint8
	starDim, starBright           uint8
	cardBg, cardEdge, cardShadow  uint8
	lime, limeInk, text, textSoft uint8
	eye, blush, white, yellow     uint8
	wing                          [2]uint8
	iconLeaf, iconStar, iconHeart uint8
	iconBox, iconPR               uint8
	order                         []struct{ x, y, z int }
	petals                        []petal
	flies                         []butterfly
	sparks                        [][3]int
	stars                         [][3]int
}

var sprite [8][8]uint8 // 0 none, 1 top, 2 left, 3 right

func init() {
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			cx, cy := float64(x)+.5, float64(y)+.5
			switch {
			case math.Abs(cx-4)/4+math.Abs(cy-2)/2 <= 1:
				sprite[y][x] = 1
			case cx < 4 && cy >= 2+cx/2 && cy <= 6+cx/2:
				sprite[y][x] = 2
			case cx >= 4 && cy >= 4-(cx-4)/2 && cy <= 8-(cx-4)/2:
				sprite[y][x] = 3
			}
		}
	}
}

func hash3(x, y, z int) uint32 {
	h := uint32(x)*73856093 ^ uint32(y)*19349663 ^ uint32(z)*83492791
	h ^= h >> 13
	h *= 0x5bd1e995
	return h ^ h>>15
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func newScene(st github.Stats) *scene {
	h := fnv.New64a()
	h.Write([]byte(st.Login))
	seed := h.Sum64()
	sc := &scene{p: newPalette(), st: st, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
	p := sc.p
	for i, hex := range []uint32{0x0e1813, 0x11201a, 0x14261d, 0x182e22, 0x1c3727} {
		sc.bg[i] = p.add(hex)
	}
	sc.starDim, sc.starBright = p.add(0x4d6b5a), p.add(0xcfe8d6)
	sc.cardBg, sc.cardEdge, sc.cardShadow = p.add(0x13241d), p.add(0x2f5242), p.add(0x0a110e)
	sc.lime, sc.limeInk = p.add(0xb8f55a), p.add(0x10201a)
	sc.text, sc.textSoft = p.add(0xe9f1dc), p.add(0x8fae9c)
	sc.eye, sc.blush = p.add(0xfff0dc), p.add(0xff8fab)
	sc.white, sc.yellow = p.add(0xffffff), p.add(0xffe066)
	sc.wing = [2]uint8{p.add(0xffb3d1), p.add(0xffd86b)}
	sc.iconLeaf, sc.iconStar, sc.iconHeart = p.add(0x7ad96c), p.add(0xffd84d), p.add(0xff8fb0)
	sc.iconBox, sc.iconPR = p.add(0xd49a66), p.add(0xb79cff)
	sc.build()
	return sc
}

func (sc *scene) set(x, y, z int, k kind, m material) {
	if x < 0 || y < 0 || z < 0 || x >= gridN || y >= gridN || z >= gridZ || sc.g[x][y][z].set {
		return
	}
	sc.g[x][y][z] = voxel{set: true, kind: k, m: m}
}

func (sc *scene) build() {
	st, p := sc.st, sc.p
	trunkM := p.mat(0x4a2e27)
	apple := p.mat(0xff5468)
	petalMats := []material{p.mat(0xf0b6cf), p.mat(0xdcd2e6), p.mat(0xf7c9dc), p.mat(0xffffff)}
	flowers := []material{p.mat(0xffe066), p.mat(0xffffff), p.mat(0xff9ec4), p.mat(0xc4a8ff)}

	// trunk: older accounts grow taller
	years := clamp(st.AccountYears, 0, 8)
	sc.trunk = top + 10 + int(math.Round(years/8*3))
	for z := top + 1; z <= sc.trunk; z++ {
		for x := ctr; x <= ctr+1; x++ {
			for y := ctr; y <= ctr+1; y++ {
				sc.set(x, y, z, solid, trunkM)
			}
		}
	}
	for _, d := range [][2]int{{-1, 0}, {2, 1}, {0, 2}, {1, -1}} {
		sc.set(ctr+d[0], ctr+d[1], top+1, solid, trunkM)
	}
	fz := top + 3
	sc.g[ctr+1][ctr][fz].decal = decalEye
	sc.g[ctr+1][ctr+1][fz].decal = decalEye

	// canopy size follows yearly contributions (public repos as a fallback)
	activity := float64(st.Contributions)
	if st.Contributions < 0 {
		activity = float64(st.Repos) * 25
	}
	r := 7.5 + 4.5*clamp(math.Log10(activity+1)/3.6, 0, 1)
	cz := float64(sc.trunk) + r*0.7
	type blob struct{ x, y, z, r float64 }
	blobs := []blob{{ctr + 1, ctr + 1, cz, r}}
	for i := 0; i < 6; i++ {
		a := 2*math.Pi*float64(i)/6 + sc.rng.Float64()*0.6
		d := r * (0.62 + sc.rng.Float64()*0.2)
		blobs = append(blobs, blob{ctr + 1 + math.Cos(a)*d, ctr + 1 + math.Sin(a)*d,
			cz + (sc.rng.Float64()-0.5)*r*0.35, r * (0.5 + sc.rng.Float64()*0.15)})
	}

	// canopy: chunky 2x2x2 blocks in sage, lavender and blossom pink
	sage := []material{p.mat(0xa9ba6f), p.mat(0x8fa35a), p.mat(0xdcd2e6)}
	pinkM := p.mat(0xf0b6cf)
	branch := p.mat(0x5a3a2c)
	pinkP := 0.18 + 0.30*clamp(float64(st.Followers)/150, 0, 1)
	type cell struct{ i, j, k int }
	var cells []cell
	for i := 0; i < gridN/2; i++ {
		for j := 0; j < gridN/2; j++ {
			for k := 0; k < gridZ/2; k++ {
				cx, cy, cz2 := float64(2*i+1), float64(2*j+1), float64(2*k+1)
				best := 9.0
				for _, b := range blobs {
					dz := (cz2 - b.z) / 0.62
					if d := math.Sqrt((cx-b.x)*(cx-b.x)+(cy-b.y)*(cy-b.y)+dz*dz) / b.r; d < best {
						best = d
					}
				}
				hh := hash3(i, j, k)
				if best+float64(hh%100)/100*0.7 >= 1 || hh%100 < 7 {
					continue
				}
				if sc.g[2*i][2*j][2*k].set {
					continue
				}
				var m material
				switch roll := sc.rng.Float64(); {
				case roll < 0.04:
					m = branch
				case roll < 0.04+pinkP:
					m = pinkM
				case roll < 0.04+pinkP+(1-pinkP)*0.45:
					m = sage[0]
				case roll < 0.04+pinkP+(1-pinkP)*0.7:
					m = sage[1]
				default:
					m = sage[2]
				}
				for dx := 0; dx < 2; dx++ {
					for dy := 0; dy < 2; dy++ {
						for dz := 0; dz < 2; dz++ {
							sc.set(2*i+dx, 2*j+dy, 2*k+dz, leaf, m)
						}
					}
				}
				cells = append(cells, cell{i, j, k})
			}
		}
	}

	// one apple per star, hanging under front-facing canopy blocks
	var cand []cell
	for _, c := range cells {
		if c.i+c.j >= ctr-3 && 2*c.k-1 > sc.trunk && !sc.g[2*c.i][2*c.j][2*c.k-1].set {
			cand = append(cand, c)
		}
	}
	sc.rng.Shuffle(len(cand), func(a, b int) { cand[a], cand[b] = cand[b], cand[a] })
	for n := 0; n < min(st.Stars, 12) && n < len(cand); n++ {
		c := cand[n]
		sc.set(2*c.i+sc.rng.IntN(2), 2*c.j+sc.rng.IntN(2), 2*c.k-1, fruit, apple)
	}

	// island: speckled grass, dirt rim with stones, tapering underside
	shadow := map[[2]int]bool{}
	for x := 0; x < gridN; x++ {
		for y := 0; y < gridN; y++ {
			for z := sc.trunk; z < gridZ; z++ {
				if sc.g[x][y][z].kind == leaf {
					shadow[[2]int{x - 3, y + 3}] = true
					break
				}
			}
		}
	}
	greens := []uint32{0x6cbd48, 0x78c850, 0x62b043, 0x84d25c}
	var grass, grassSh [4]material
	for i, g := range greens {
		grass[i], grassSh[i] = p.mat(g), p.mat(shade(g, 0.72))
	}
	dirt := []material{p.mat(0x7a4a2e), p.mat(0x6b3f27), p.mat(0x84552f)}
	stone := p.mat(0x77776f)
	deep := []material{p.mat(0x4a3226), p.mat(0x3c281f)}
	type layer struct {
		z int
		r float64
	}
	for _, l := range []layer{{6, 13}, {5, 12.4}, {4, 10.6}, {3, 8.4}, {2, 6.2}, {1, 4}, {0, 2}} {
		for x := 0; x < gridN; x++ {
			for y := 0; y < gridN; y++ {
				d := math.Hypot(float64(x-ctr)-.5, float64(y-ctr)-.5)
				if d > l.r+float64(hash3(x, y, l.z)%3)*0.3-0.3 {
					continue
				}
				h := int(hash3(x, y, l.z+40))
				var m material
				switch {
				case l.z == top:
					m = grass[h%4]
					if shadow[[2]int{x, y}] {
						m = grassSh[h%4]
					}
				case l.z == top-1:
					m = dirt[h%3]
					if h%9 == 0 {
						m = stone
					}
				case l.z >= 3:
					m = dirt[h%3]
					if h%7 == 0 {
						m = stone
					}
				default:
					m = deep[h%2]
				}
				sc.set(x, y, l.z, solid, m)
				// little grass bumps on the rim
				if l.z == top && d > 10.5 && h%11 == 0 {
					sc.set(x, y, top+1, solid, grass[(h/3)%4])
				}
			}
		}
	}

	// flowers on the grass, one per repo
	var spots [][2]int
	for x := 0; x < gridN; x++ {
		for y := 0; y < gridN; y++ {
			if sc.g[x][y][top].set && !sc.g[x][y][top+1].set && math.Hypot(float64(x-ctr), float64(y-ctr)) < 11 &&
				(abs(x-ctr) > 3 || abs(y-ctr) > 3) && x+y > 2*ctr-8 {
				spots = append(spots, [2]int{x, y})
			}
		}
	}
	sc.rng.Shuffle(len(spots), func(i, j int) { spots[i], spots[j] = spots[j], spots[i] })
	for i := 0; i < min(st.Repos, 12) && i < len(spots); i++ {
		sc.set(spots[i][0], spots[i][1], top+1, solid, flowers[i%len(flowers)])
	}

	// render order: back to front
	for s := 0; s < 2*gridN; s++ {
		for x := 0; x < gridN; x++ {
			y := s - x
			if y < 0 || y >= gridN {
				continue
			}
			for z := 0; z < gridZ; z++ {
				if sc.g[x][y][z].set {
					sc.order = append(sc.order, struct{ x, y, z int }{x, y, z})
				}
			}
		}
	}

	// centre the picture vertically; keep it right of the stats card
	minY, maxY := 1<<30, -1<<30
	for _, o := range sc.order {
		sy := (o.x+o.y)*2 - o.z*4
		minY, maxY = min(minY, sy), max(maxY, sy+8)
	}
	sc.oy = (H-(maxY-minY))/2 - minY
	sc.ox = 246
	sc.cy = sc.oy + (2*ctr+2)*2 - int(cz)*4

	// critters
	for i := 0; i < 16; i++ {
		sc.petals = append(sc.petals, petal{
			x: sc.ox - 70 + sc.rng.IntN(140), y: sc.cy - 20 + sc.rng.IntN(30),
			phase: float64(i) / 16, sway: 2 + sc.rng.Float64()*3, m: petalMats[sc.rng.IntN(len(petalMats))]})
	}
	for i := 0; i < int(clamp(float64(st.PullRequests)/15, 1, 4)); i++ {
		sc.flies = append(sc.flies, butterfly{cx: sc.ox - 80 + i*52, cy: sc.cy + 20 + (i%2)*14, rx: 16 + i*3, ry: 8,
			phase: float64(i) * 0.27, wing: sc.wing[i%2]})
	}
	for i := 0; i < 6; i++ {
		sc.sparks = append(sc.sparks, [3]int{sc.ox - 60 + sc.rng.IntN(120), sc.cy - 40 + sc.rng.IntN(60), sc.rng.IntN(Frames)})
	}
	for i := 0; i < 45; i++ {
		sc.stars = append(sc.stars, [3]int{sc.rng.IntN(W), sc.rng.IntN(H), sc.rng.IntN(Frames)})
	}
}

// ---- drawing ---------------------------------------------------------

type canvas struct{ *image.Paletted }

func (c canvas) put(x, y int, i uint8) {
	if x >= 0 && y >= 0 && x < W && y < H {
		c.Pix[y*c.Stride+x] = i
	}
}

func (c canvas) rect(x, y, w, h int, i uint8) {
	for yy := y; yy < y+h; yy++ {
		for xx := x; xx < x+w; xx++ {
			c.put(xx, yy, i)
		}
	}
}

func (c canvas) bitmap(x, y, scale int, bm string, col uint8) {
	for dy, row := range rows(bm) {
		for dx, ch := range row {
			if ch == '#' {
				c.rect(x+dx*scale, y+dy*scale, scale, scale, col)
			}
		}
	}
}

func (c canvas) text(x, y, scale int, s string, col uint8) {
	for _, r := range s {
		for dy, row := range glyph(r) {
			for dx, ch := range row {
				if ch == '#' {
					c.rect(x+dx*scale, y+dy*scale, scale, scale, col)
				}
			}
		}
		x += 4 * scale
	}
}

func (c canvas) cube(sx, sy int, v voxel) {
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			switch sprite[y][x] {
			case 1:
				c.put(sx-4+x, sy+y, v.m.top)
			case 2:
				c.put(sx-4+x, sy+y, v.m.left)
			case 3:
				c.put(sx-4+x, sy+y, v.m.right)
			}
		}
	}
}

func (sc *scene) decal(c canvas, sx, sy int, blink bool) {
	px := func(x, y int, i uint8) { c.put(sx-4+x, sy+y, i) }
	if blink {
		px(5, 4, sc.eye)
		px(6, 4, sc.eye)
	} else {
		px(5, 3, sc.eye)
		px(5, 4, sc.eye)
		px(6, 3, sc.eye)
		px(6, 4, sc.eye)
	}
	px(5, 6, sc.blush)
	px(6, 6, sc.blush)
}

func fmtNum(n int) string {
	switch {
	case n < 0:
		return "?"
	case n >= 1_000_000:
		return trim(float64(n)/1e6) + "M"
	case n >= 10_000:
		return itoa(n/1000) + "K"
	case n >= 1000:
		return trim(float64(n)/1e3) + "K"
	}
	return itoa(n)
}

func trim(f float64) string {
	s := itoa(int(f*10+.5) / 10)
	if t := int(f*10+.5) % 10; t != 0 {
		s += "." + itoa(t)
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func (sc *scene) frame(k int) *image.Paletted {
	c := canvas{image.NewPaletted(image.Rect(0, 0, W, H), sc.p.list)}
	t := float64(k) / Frames
	tau := 2 * math.Pi

	// night-forest sky with dithered bands
	bands := []int{40, 80, 120, 156}
	for y := 0; y < H; y++ {
		b := 0
		for b < len(bands) && y >= bands[b] {
			b++
		}
		for x := 0; x < W; x++ {
			i := sc.bg[b]
			if b > 0 && y-bands[b-1] < 2 && (x+y)%2 == 0 {
				i = sc.bg[b-1]
			}
			c.Pix[y*c.Stride+x] = i
		}
	}
	for _, s := range sc.stars {
		ph := (k - s[2] + Frames) % Frames
		if ph < 6 {
			c.put(s[0], s[1], sc.starBright)
		} else {
			c.put(s[0], s[1], sc.starDim)
		}
	}

	sc.drawFlies(c, k, false)

	// the floating island and tree
	bob := int(math.Round(math.Sin(tau*t) * 1.6))
	blink := k >= 20 && k <= 22
	for _, o := range sc.order {
		v := sc.g[o.x][o.y][o.z]
		sx := (o.x-o.y)*4 + sc.ox
		sy := (o.x+o.y)*2 - o.z*4 + sc.oy + bob
		if v.kind == leaf || v.kind == fruit {
			h := float64(o.z-sc.trunk) / 12
			sx += int(math.Round(math.Sin(tau*t-float64(o.z)*0.15) * (0.5 + 1.5*clamp(h, 0, 1))))
			if v.kind == fruit {
				sy += int(math.Round(math.Sin(tau*(t+float64(o.x)*0.13)) * 0.8))
			}
		}
		c.cube(sx, sy, v)
		if v.decal != 0 {
			sc.decal(c, sx, sy, blink)
		}
	}

	// drifting petals
	for _, f := range sc.petals {
		p := math.Mod(t+f.phase, 1)
		x := f.x + int(math.Round(math.Sin(tau*(p*2))*f.sway))
		y := f.y + int(p*90)
		if p > 0.9 {
			c.put(x, y, f.m.top)
			continue
		}
		c.rect(x, y, 2, 1, f.m.top)
		c.rect(x, y+1, 2, 1, f.m.right)
		if k%2 == 0 {
			c.put(x+2, y, f.m.left)
		}
	}
	for _, s := range sc.sparks {
		ph := (k - s[2] + Frames) % Frames
		if ph < 4 {
			c.put(s[0], s[1], sc.white)
			if ph == 1 || ph == 2 {
				c.put(s[0]-1, s[1], sc.yellow)
				c.put(s[0]+1, s[1], sc.yellow)
				c.put(s[0], s[1]-1, sc.yellow)
				c.put(s[0], s[1]+1, sc.yellow)
			}
		}
	}
	sc.drawFlies(c, k, true)
	sc.drawCard(c)
	return upscale(c.Paletted)
}

func (sc *scene) drawFlies(c canvas, k int, front bool) {
	for i, b := range sc.flies {
		if (i%2 == 1) != front {
			continue
		}
		a := 2*math.Pi*(float64(k)/Frames) + b.phase*2*math.Pi
		x := b.cx + int(math.Round(math.Cos(a)*float64(b.rx)))
		y := b.cy + int(math.Round(math.Sin(2*a)*float64(b.ry)))
		if (k/2)%2 == 0 {
			c.rect(x-3, y-2, 2, 2, b.wing)
			c.rect(x+1, y-2, 2, 2, b.wing)
			c.rect(x-2, y, 2, 1, b.wing)
			c.rect(x, y, 2, 1, b.wing)
		} else {
			c.rect(x-2, y-1, 2, 2, b.wing)
			c.rect(x, y-1, 2, 2, b.wing)
		}
		c.rect(x-1, y-2, 1, 3, sc.text)
	}
}

func (sc *scene) drawCard(c canvas) {
	const x0, y0, x1, y1 = 8, 8, 158, H - 8
	c.rect(x0+3, y0+3, x1-x0, y1-y0, sc.cardShadow)
	c.rect(x0, y0, x1-x0, y1-y0, sc.cardEdge)
	c.rect(x0+2, y0+2, x1-x0-4, y1-y0-4, sc.cardBg)

	st := sc.st
	c.rect(x0+8, y0+7, textWidth("HARVEST", 2)+8, 14, sc.lime)
	c.text(x0+12, y0+9, 2, "HARVEST", sc.limeInk)

	scale := 3
	for scale > 1 && textWidth(st.Login, scale) > x1-x0-16 {
		scale--
	}
	c.text(x0+8, y0+28, scale, st.Login, sc.text)
	c.text(x0+8, y0+47, 2, "GROWN IN "+trim(st.AccountYears)+" YRS", sc.textSoft)

	type row struct {
		icon  string
		col   uint8
		n     int
		label string
	}
	data := []row{
		{iconLeaf, sc.iconLeaf, st.Contributions, "CONTRIBS"},
		{iconStar, sc.iconStar, st.Stars, "STARS"},
		{iconHeart, sc.iconHeart, st.Followers, "FOLLOWERS"},
		{iconBox, sc.iconBox, st.Repos, "REPOS"},
		{iconDiamnd, sc.iconPR, st.PullRequests, "PULLS"},
	}
	y := y0 + 64
	for _, r := range data {
		c.bitmap(x0+8, y+2, 2, r.icon, r.col)
		c.text(x0+22, y, 3, fmtNum(r.n), sc.text)
		c.text(x0+22+5*12+4, y+5, 1, r.label, sc.textSoft)
		y += 19
	}
	if st.TopLanguage != "" {
		c.text(x0+8, y+3, 2, "LOVES "+st.TopLanguage, sc.lime)
	}
}

func upscale(src *image.Paletted) *image.Paletted {
	dst := image.NewPaletted(image.Rect(0, 0, W*Scale, H*Scale), src.Palette)
	for y := 0; y < H; y++ {
		row := src.Pix[y*src.Stride : y*src.Stride+W]
		line := dst.Pix[y*Scale*dst.Stride : y*Scale*dst.Stride+W*Scale]
		for x, v := range row {
			for s := 0; s < Scale; s++ {
				line[x*Scale+s] = v
			}
		}
		for s := 1; s < Scale; s++ {
			copy(dst.Pix[(y*Scale+s)*dst.Stride:], line)
		}
	}
	return dst
}

// Render grows a tree for the given stats and returns an endlessly looping GIF.
func Render(st github.Stats) *gif.GIF {
	sc := newScene(st)
	g := &gif.GIF{LoopCount: 0}
	for k := 0; k < Frames; k++ {
		g.Image = append(g.Image, sc.frame(k))
		g.Delay = append(g.Delay, Delay)
		g.Disposal = append(g.Disposal, gif.DisposalNone)
	}
	return g
}
