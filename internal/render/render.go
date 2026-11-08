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
	W, H   = 220, 128 // low-res canvas
	Scale  = 3        // final pixels per canvas pixel
	Frames = 36
	Delay  = 6 // centiseconds
	gridN  = 21
	gridZ  = 40
	ctr    = gridN / 2
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
	// darker sides drift slightly towards violet, which reads warmer and cuter.
	r = r * f
	g = g * f * 0.99
	b = math.Min(255, b*f*1.04+(1-f)*30)
	return uint32(r)<<16 | uint32(g)<<8 | uint32(b)
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

const (
	decalEye uint8 = iota + 1
	decalMouth
)

type scene struct {
	p     *palette
	g     [gridN][gridN][gridZ]voxel
	rng   *rand.Rand
	st    github.Stats
	trunk int // top z of trunk
	oy    int
	ox    int

	// palette entries
	sky                           [5]uint8
	sun, sunGlow, cloud, cloudSh  uint8
	ink, inkSoft, card, cardEdge  uint8
	pink, white, yellow, leafSpec uint8
	leafMats                      [4]material
	wing                          [2]uint8
	faceInk, blush                uint8
	boxCol, purple                uint8
	order                         []struct{ x, y, z int }
	falling                       []fallingLeaf
	flies                         []butterfly
	sparks                        [][3]int
}

type fallingLeaf struct {
	x, y   int
	phase  float64
	sway   float64
	colour uint8
}

type butterfly struct {
	cx, cy, rx, ry int
	phase          float64
	wing           uint8
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

func newScene(st github.Stats) *scene {
	h := fnv.New64a()
	h.Write([]byte(st.Login))
	seed := h.Sum64()
	sc := &scene{p: newPalette(), st: st, rng: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))}
	p := sc.p
	for i, hex := range []uint32{0xb8e2ff, 0xc9ebff, 0xdaf3ff, 0xe8f8ff, 0xf6fcff} {
		sc.sky[i] = p.add(hex)
	}
	sc.sun, sc.sunGlow = p.add(0xfff1a1), p.add(0xfff8cf)
	sc.cloud, sc.cloudSh = p.add(0xffffff), p.add(0xe2f0fb)
	sc.ink, sc.inkSoft = p.add(0x3d3357), p.add(0x7a6f99)
	sc.card, sc.cardEdge = p.add(0xfffaf3), p.add(0xf0d9c8)
	sc.pink, sc.white, sc.yellow = p.add(0xff9ec4), p.add(0xffffff), p.add(0xffe066)
	sc.leafSpec = p.add(0xd8ff9c)
	sc.wing = [2]uint8{p.add(0xffb3d1), p.add(0xffd86b)}
	sc.faceInk, sc.blush = p.add(0x2b2142), p.add(0xff8fab)
	for i, hex := range []uint32{0x2f9a52, 0x4fbf62, 0x7ad96c, 0xa9f08c} {
		sc.leafMats[i] = p.mat(hex)
	}
	sc.boxCol, sc.purple = p.add(0xb98458), p.add(0xa98bff)
	sc.build()
	return sc
}

func (sc *scene) set(x, y, z int, k kind, m material) {
	if x < 0 || y < 0 || z < 0 || x >= gridN || y >= gridN || z >= gridZ {
		return
	}
	sc.g[x][y][z] = voxel{set: true, kind: k, m: m}
}

func (sc *scene) build() {
	st, p := sc.st, sc.p
	grassA, grassB := p.mat(0x8fd86a), p.mat(0x84cf62)
	dirt, dirtB := p.mat(0xc99361), p.mat(0xb98250)
	trunkM := p.mat(0xb98458)
	blossom := p.mat(0xffb3d3)
	apple := p.mat(0xff5468)
	petals := []material{p.mat(0xffe066), p.mat(0xffffff), p.mat(0xff9ec4), p.mat(0xc4a8ff)}

	// floating island
	type layer struct {
		z int
		r float64
	}
	const top = 4
	for i, l := range []layer{{4, 7.3}, {3, 6.7}, {2, 5.2}, {1, 3.5}, {0, 1.9}} {
		for x := 0; x < gridN; x++ {
			for y := 0; y < gridN; y++ {
				if math.Hypot(float64(x-ctr), float64(y-ctr)) > l.r {
					continue
				}
				m := dirt
				if (x+y+i)%3 == 0 {
					m = dirtB
				}
				if l.z == top {
					m = grassA
					if hash3(x, y, 1)%3 == 0 {
						m = grassB
					}
				}
				sc.set(x, y, l.z, solid, m)
			}
		}
	}

	// trunk: older accounts grow taller
	years := clamp(st.AccountYears, 0, 8)
	th := 6 + int(math.Round(years/8*3))
	sc.trunk = top + th
	for z := top + 1; z <= sc.trunk; z++ {
		for x := ctr - 1; x <= ctr+1; x++ {
			for y := ctr - 1; y <= ctr+1; y++ {
				sc.set(x, y, z, solid, trunkM)
			}
		}
	}
	// root flare
	for _, d := range [][2]int{{2, 0}, {-2, 0}, {0, 2}, {0, -2}} {
		sc.set(ctr+d[0], ctr+d[1], top+1, solid, trunkM)
	}
	// kawaii face on the +x side of the trunk
	fz := top + 3
	sc.g[ctr+1][ctr-1][fz].decal = decalEye
	sc.g[ctr+1][ctr+1][fz].decal = decalEye
	sc.g[ctr+1][ctr][fz].decal = decalMouth

	// canopy size comes from yearly contributions (public repos as a fallback)
	activity := float64(st.Contributions)
	if st.Contributions < 0 {
		activity = float64(st.Repos) * 25
	}
	r := 3.8 + 2.6*clamp(math.Log10(activity+1)/3.6, 0, 1)
	cz := float64(sc.trunk) + r*0.72
	type blob struct{ x, y, z, r float64 }
	blobs := []blob{{ctr, ctr, cz, r}}
	nb := 5
	for i := 0; i < nb; i++ {
		a := 2*math.Pi*float64(i)/float64(nb) + sc.rng.Float64()*0.5
		d := r * (0.6 + sc.rng.Float64()*0.15)
		blobs = append(blobs, blob{ctr + math.Cos(a)*d, ctr + math.Sin(a)*d,
			cz - r*0.05 + (sc.rng.Float64()-0.3)*r*0.3, r * (0.55 + sc.rng.Float64()*0.12)})
	}
	for x := 0; x < gridN; x++ {
		for y := 0; y < gridN; y++ {
			for z := 0; z < gridZ; z++ {
				if sc.g[x][y][z].set {
					continue
				}
				best, bi := 9.0, -1
				for i, b := range blobs {
					dx, dy, dz := float64(x)-b.x, float64(y)-b.y, (float64(z)-b.z)/0.86
					d := math.Sqrt(dx*dx+dy*dy+dz*dz) / b.r
					if d < best {
						best, bi = d, i
					}
				}
				if best > 1 {
					continue
				}
				b := blobs[bi]
				tone := 1 + int(hash3(x/2, y/2, bi)%2)
				if float64(z) > b.z+b.r*0.3 {
					tone = 3
				} else if float64(z) < b.z-b.r*0.35 {
					tone = 0
				}
				sc.set(x, y, z, leaf, sc.leafMats[tone])
			}
		}
	}

	// candidate surface voxels that face the camera (+x or +y free)
	var cand [][3]int
	for x := 1; x < gridN-1; x++ {
		for y := 1; y < gridN-1; y++ {
			for z := int(cz - r*0.5); z < gridZ-1; z++ {
				v := sc.g[x][y][z]
				if v.set && v.kind == leaf && (!sc.g[x+1][y][z].set || !sc.g[x][y+1][z].set) &&
					sc.g[x][y][z+1].set && x+y >= 2*ctr-3 {
					cand = append(cand, [3]int{x, y, z})
				}
			}
		}
	}
	sc.rng.Shuffle(len(cand), func(i, j int) { cand[i], cand[j] = cand[j], cand[i] })
	take := func(n int, m material, k kind) {
		for ; n > 0 && len(cand) > 0; n-- {
			c := cand[0]
			cand = cand[1:]
			sc.set(c[0], c[1], c[2], k, m)
		}
	}
	take(min(st.Stars, 12), apple, fruit)        // one apple per star
	take(min(st.Followers/4, 16), blossom, leaf) // blossoms for followers

	// flowers on the grass, one per repo
	var spots [][2]int
	for x := 0; x < gridN; x++ {
		for y := 0; y < gridN; y++ {
			if sc.g[x][y][top].set && !sc.g[x][y][top+1].set && math.Hypot(float64(x-ctr), float64(y-ctr)) < 6.6 &&
				(abs(x-ctr) > 2 || abs(y-ctr) > 2) && x+y > 2*ctr-6 {
				spots = append(spots, [2]int{x, y})
			}
		}
	}
	sc.rng.Shuffle(len(spots), func(i, j int) { spots[i], spots[j] = spots[j], spots[i] })
	for i := 0; i < min(st.Repos, 9) && i < len(spots); i++ {
		sc.set(spots[i][0], spots[i][1], top+1, solid, petals[i%len(petals)])
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

	// centre the picture vertically
	minY, maxY := 1<<30, -1<<30
	for _, o := range sc.order {
		sy := (o.x+o.y)*2 - o.z*4
		minY, maxY = min(minY, sy), max(maxY, sy+8)
	}
	sc.oy = (H-(maxY-minY))/2 - minY
	sc.ox = 164

	// critters
	canopyTop := sc.oy + (2*ctr)*2 - int(cz+r)*4
	for i := 0; i < 6; i++ {
		sc.falling = append(sc.falling, fallingLeaf{
			x: sc.ox - 34 + sc.rng.IntN(70), y: max(canopyTop+20, 30) + sc.rng.IntN(15),
			phase: float64(i) / 6, sway: 2 + sc.rng.Float64()*2, colour: sc.leafMats[1+sc.rng.IntN(3)].top})
	}
	for i := 0; i < int(clamp(float64(st.PullRequests)/15, 1, 4)); i++ {
		sc.flies = append(sc.flies, butterfly{cx: sc.ox - 50 + i*34, cy: 62 + (i%2)*12 - 10, rx: 12 + i*2, ry: 6,
			phase: float64(i) * 0.27, wing: sc.wing[i%2]})
	}
	for i := 0; i < 5; i++ {
		sc.sparks = append(sc.sparks, [3]int{sc.ox - 44 + sc.rng.IntN(90), canopyTop + sc.rng.IntN(60), sc.rng.IntN(Frames)})
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
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

func (c canvas) bitmap(x, y int, bm string, col uint8) {
	for dy, row := range rows(bm) {
		for dx, ch := range row {
			if ch == '#' {
				c.put(x+dx, y+dy, col)
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

func (c canvas) disc(cx, cy int, r float64, i uint8) {
	for y := cy - int(r) - 1; y <= cy+int(r)+1; y++ {
		for x := cx - int(r) - 1; x <= cx+int(r)+1; x++ {
			if math.Hypot(float64(x-cx), float64(y-cy)) <= r {
				c.put(x, y, i)
			}
		}
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

func (sc *scene) decal(c canvas, sx, sy int, d uint8, blink bool) {
	px := func(x, y int, i uint8) { c.put(sx-4+x, sy+y, i) }
	switch d {
	case decalEye:
		if blink {
			px(5, 4, sc.faceInk)
			px(6, 4, sc.faceInk)
		} else {
			px(5, 3, sc.faceInk)
			px(5, 4, sc.faceInk)
			px(6, 4, sc.faceInk)
			px(6, 3, sc.white)
		}
		px(5, 6, sc.blush)
		px(6, 6, sc.blush)
	case decalMouth:
		px(4, 5, sc.faceInk)
		px(5, 6, sc.faceInk)
		px(6, 6, sc.faceInk)
		px(7, 5, sc.faceInk)
	}
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

	// sky with dithered bands
	bands := []int{26, 52, 78, 100}
	for y := 0; y < H; y++ {
		b := 0
		for b < len(bands) && y >= bands[b] {
			b++
		}
		for x := 0; x < W; x++ {
			i := sc.sky[b]
			if b > 0 && y-bands[b-1] < 2 && (x+y)%2 == 0 {
				i = sc.sky[b-1]
			}
			c.Pix[y*c.Stride+x] = i
		}
	}
	// sun
	c.disc(W-22, 20, 12, sc.sunGlow)
	c.disc(W-22, 20, 8, sc.sun)
	// clouds that gently bob
	for i, cl := range [][3]int{{30, 112, 0}, {62, 100, 1}, {18, 14, 0}, {96, 24, 1}} {
		_ = i
		dy := int(math.Round(math.Sin(tau*(t+float64(cl[2])*.5)) * 1.2))
		x, y := cl[0], cl[1]+dy
		if y > 90 { // lower clouds sit behind the card, so they peek out under the island
			continue
		}
		c.disc(x, y+2, 4, sc.cloudSh)
		c.disc(x+7, y+3, 3, sc.cloudSh)
		c.disc(x, y, 4, sc.cloud)
		c.disc(x+6, y+1, 3, sc.cloud)
		c.disc(x+3, y-2, 4, sc.cloud)
	}
	c.disc(sc.ox-62, 110, 5, sc.cloud)
	c.disc(sc.ox-55, 111, 4, sc.cloud)
	c.disc(sc.ox+58, 106, 5, sc.cloud)
	c.disc(sc.ox+65, 107, 4, sc.cloud)

	// butterflies behind the tree
	sc.drawFlies(c, k, false)

	// tree
	bob := int(math.Round(math.Sin(tau*t) * 1.4))
	blink := k >= 20 && k <= 22
	for _, o := range sc.order {
		v := sc.g[o.x][o.y][o.z]
		sx := (o.x-o.y)*4 + sc.ox
		sy := (o.x+o.y)*2 - o.z*4 + sc.oy + bob
		if v.kind == leaf || v.kind == fruit {
			// sway grows with height above the trunk
			h := float64(o.z-sc.trunk) / 9
			sx += int(math.Round(math.Sin(tau*t-float64(o.z)*0.18) * (0.4 + 1.6*clamp(h, 0, 1))))
			if v.kind == fruit {
				sy += int(math.Round(math.Sin(tau*(t+float64(o.x)*0.13)) * 0.8))
			}
		}
		c.cube(sx, sy, v)
		if v.decal != 0 {
			sc.decal(c, sx, sy, v.decal, blink)
		}
	}

	// falling leaves
	for _, f := range sc.falling {
		p := math.Mod(t+f.phase, 1)
		x := f.x + int(math.Round(math.Sin(tau*(p*2))*f.sway))
		y := f.y + int(p*58)
		if p > 0.88 {
			c.put(x, y, f.colour)
		} else {
			c.rect(x, y, 2, 2, f.colour)
			c.put(x+1, y+1, sc.leafMats[0].top)
		}
	}
	// sparkles
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
	sc.drawFlies(c, k, true) // every other butterfly flutters in front
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
			c.put(x-2, y-1, b.wing)
			c.put(x-1, y-1, b.wing)
			c.put(x+1, y-1, b.wing)
			c.put(x+2, y-1, b.wing)
			c.put(x-1, y, b.wing)
			c.put(x+1, y, b.wing)
		} else {
			c.put(x-1, y-1, b.wing)
			c.put(x+1, y-1, b.wing)
			c.put(x-1, y, b.wing)
			c.put(x+1, y, b.wing)
		}
		c.put(x, y, sc.faceInk)
		c.put(x, y-1, sc.faceInk)
	}
}

func (sc *scene) drawCard(c canvas) {
	const x0, y0, x1, y1 = 4, 4, 112, H - 5
	c.rect(x0+1, y0, x1-x0-2, y1-y0, sc.cardEdge)
	c.rect(x0, y0+1, x1-x0, y1-y0-2, sc.cardEdge)
	c.rect(x0+2, y0+1, x1-x0-4, y1-y0-2, sc.card)
	c.rect(x0+1, y0+2, x1-x0-2, y1-y0-4, sc.card)

	st := sc.st
	name := st.Login
	scale := 2
	if textWidth(name, 2) > x1-x0-14 {
		scale = 1
	}
	c.text(x0+8, y0+7, scale, name, sc.ink)
	sub := "GROWN IN " + trim(st.AccountYears) + " YRS"
	c.text(x0+8, y0+7+5*scale+3, 1, sub, sc.inkSoft)

	type row struct {
		icon  string
		col   uint8
		n     int
		label string
	}
	rowsData := []row{
		{iconLeaf, sc.leafMats[1].left, st.Contributions, "CONTRIBS"},
		{iconStar, sc.yellow, st.Stars, "STARS"},
		{iconHeart, sc.pink, st.Followers, "FOLLOWERS"},
		{iconBox, sc.boxCol, st.Repos, "REPOS"},
		{iconDiamnd, sc.purple, st.PullRequests, "PULLS"},
	}
	y := y0 + 32
	for _, r := range rowsData {
		c.bitmap(x0+8, y+3, r.icon, r.col)
		c.text(x0+17, y, 2, fmtNum(r.n), sc.ink)
		c.text(x0+17+5*8+2, y+5, 1, r.label, sc.inkSoft)
		y += 14
	}
	if st.TopLanguage != "" {
		c.text(x0+8, y+1, 1, "LOVES "+st.TopLanguage, sc.ink)
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
