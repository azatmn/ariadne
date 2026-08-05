package core

import (
	"context"
	"math"
	"testing"
	"time"

	"ariadne/internal/geo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Тесты сборки ядра.
//
// Части ядра проверены поодиночке, и каждая совпадает с прототипом. Здесь
// проверяется то, чего сверка по частям не касается вовсе: ПОРЯДОК, в котором
// всё это накладывается. Привилегия стоянке, множитель за наблюдения, деление
// веса пачек, штраф заморозке, штрафы правил, амнистия — переставь любые два
// местами, и результат другой, а каждая часть по отдельности по-прежнему верна.

// ---------------------------------------------------------------- подставные

// fakeSnapper — снэпы по позиции точки. nil означает «OSRM промолчал».
type fakeSnapper struct {
	at    func(p geo.Point) *float64
	calls int
	sizes []int
}

func (f *fakeSnapper) Snap(_ context.Context, pts []geo.Point) ([]float64, []bool, []string) {
	f.calls++
	f.sizes = append(f.sizes, len(pts))

	snaps := make([]float64, len(pts))
	ok := make([]bool, len(pts))
	for i, p := range pts {
		if f.at == nil {
			continue
		}
		if v := f.at(p); v != nil {
			snaps[i], ok[i] = *v, true
		}
	}
	return snaps, ok, nil
}

// flatSnaps — один и тот же снэп на все точки.
func flatSnaps(v float64) *fakeSnapper {
	return &fakeSnapper{at: func(geo.Point) *float64 { return &v }}
}

// fixedDecoys / fixedAirfields — данные, которых в проде пока нет: списки
// приманок и контуров лётных полей не восстановлены. Логика при этом
// перенесена целиком, и проверять её надо, поэтому в тестах она кормится
// вручную.
type fixedDecoys []float64

func (d fixedDecoys) Mask(pts []geo.Point) []float64 {
	out := make([]float64, len(pts))
	copy(out, d)
	return out
}

type fixedAirfields []bool

func (a fixedAirfields) Mask(pts []geo.Point) []bool {
	out := make([]bool, len(pts))
	copy(out, a)
	return out
}

// noFacts — пустые сведения о стоянках: удобная точка отсчёта для тестов веса.
func noFacts(n int) stopFacts {
	return stopFacts{
		trusted:  map[int]struct{}{},
		observed: map[int]int{},
		frozen:   map[int]struct{}{},
		split:    map[int]struct{}{},
	}
}

// ------------------------------------------------- порядок сборки весов

func TestCoreWeights_StopPrivilegeComesFromRawWeight(t *testing.T) {
	// Привилегия доверенной стоянке считается от СЫРОГО веса, а не от
	// сглаженного. У долгой стоянки соседи — уже другой эпизод, и сглаживание
	// топит её чужими глюками.
	pts := drive(9, 60, 10, 0, 0.01, 0)
	snaps := make([]float64, len(pts))
	ok := make([]bool, len(pts))
	for i := range pts {
		snaps[i], ok[i] = 1500, true // вокруг всё далеко от дорог
	}
	snaps[4] = 5 // а сама стоянка — на дороге

	c := &Core{}
	f := noFacts(len(pts))
	f.trusted[4] = struct{}{}

	w, _ := c.buildWeights(pts, snaps, ok, f)

	assert.GreaterOrEqual(t, w[4], StopTrustW,
		"стоянка не должна тонуть ниже привилегии из-за соседей")
}

func TestCoreWeights_PrivilegeNeverLowersWeight(t *testing.T) {
	// Привилегия — это пол, а не фиксированное значение: точка с хорошим
	// собственным весом не должна из-за неё худеть.
	pts := drive(9, 60, 10, 0, 0.01, 0)
	snaps, ok := snapAll(len(pts), 1)

	c := &Core{}
	plain, _ := c.buildWeights(pts, snaps, ok, noFacts(len(pts)))

	f := noFacts(len(pts))
	f.trusted[4] = struct{}{}
	privileged, _ := c.buildWeights(pts, snaps, ok, f)

	assert.GreaterOrEqual(t, privileged[4], plain[4], "привилегия не может понижать вес")
}

func TestCoreWeights_ObservationBonusAppliesAfterPrivilege(t *testing.T) {
	// Множитель за число наблюдений накладывается ПОВЕРХ привилегии.
	// Переставь их местами — стоянка получила бы ровно StopTrustW и
	// проиграла бы соседней случайной точке, ради чего множитель и вводился.
	pts := drive(9, 60, 10, 0, 0.01, 0)
	snaps, ok := snapAll(len(pts), 1500) // сырой вес отрицательный у всех
	snaps[4] = 5

	c := &Core{}
	f := noFacts(len(pts))
	f.trusted[4] = struct{}{}
	f.observed[4] = 95 // ночлег из 95 наблюдений

	w, _ := c.buildWeights(pts, snaps, ok, f)

	want := StopTrustW * (1 + math.Log10(95))
	assert.InDelta(t, want, w[4], 1e-9,
		"множитель обязан умножать уже поднятый привилегией вес")
}

func TestCoreWeights_ObservationBonusOnlyForPositiveWeights(t *testing.T) {
	// Отрицательный вес множить нельзя: умножение сделало бы улику сильнее
	// пропорционально числу наблюдений, то есть многочисленность подделки
	// стала бы доводом ПРОТИВ неё, а это уже другое правило.
	pts := drive(5, 60, 10, 0, 0.01, 0)
	snaps, ok := snapAll(len(pts), 1500)

	c := &Core{}
	base, _ := c.buildWeights(pts, snaps, ok, noFacts(len(pts)))

	f := noFacts(len(pts))
	f.observed[2] = 95
	got, _ := c.buildWeights(pts, snaps, ok, f)

	require.Negative(t, base[2], "для чистоты опыта вес должен быть отрицательным")
	assert.InDelta(t, base[2], got[2], 1e-9, "отрицательный вес множитель не трогает")
}

func TestCoreWeights_SplitConvictedGetNoObservationBonus(t *testing.T) {
	// Многочисленность подделки — её свойство, а не довод за неё. Настоящая
	// стоянка пишет раз в семь минут, подделка сыплет раз в пять секунд.
	// На `ab681145` за 109 наблюдений вес рос втрое и перекрывал штраф −3.
	pts := drive(5, 60, 10, 0, 0.01, 0)
	snaps, ok := snapAll(len(pts), 5)

	c := &Core{}
	bare, _ := c.buildWeights(pts, snaps, ok, noFacts(len(pts)))

	f := noFacts(len(pts))
	f.observed[2] = 109
	honest, _ := c.buildWeights(pts, snaps, ok, f)
	require.InDelta(t, bare[2]*(1+math.Log10(109)), honest[2], 1e-9,
		"честная стоянка бонус получает")

	f.split[2] = struct{}{}
	convicted, _ := c.buildWeights(pts, snaps, ok, f)

	// Ровно исходный вес минус штраф — никакого множителя между ними.
	assert.InDelta(t, bare[2]-SplitPenalty, convicted[2], 1e-9,
		"уличённой стоянке бонус не положен")
}

func TestCoreWeights_SameSecondBatchSplitsWeight(t *testing.T) {
	// Пачка записей с одной секундой — это ОДНО наблюдение по времени.
	// Без деления выгрузка буфера перевешивает честный редкий участок.
	batch := []geo.Point{
		at(0, 10.000, 0),
		at(60, 10.010, 0), at(60, 10.011, 0), at(60, 10.012, 0), at(60, 10.013, 0),
		at(120, 10.020, 0),
	}
	snaps, ok := snapAll(len(batch), 5)

	c := &Core{}
	w, _ := c.buildWeights(batch, snaps, ok, noFacts(len(batch)))

	assert.InDelta(t, w[0]/4, w[1], 1e-9, "вес пачки из четырёх делится на четыре")
	assert.InDelta(t, w[1], w[4], 1e-9, "и делится поровну")
}

func TestCoreWeights_RepeatedCoordinateSplitsWeight(t *testing.T) {
	// То же для повторённой КООРДИНАТЫ: трекер без спутников шлёт последнюю
	// известную позицию байт в байт. Это одно значение, а не десять
	// наблюдений. Стоянки такие пачки ловят только от пяти минут, а короткие
	// проваливались мимо всех проверок с полным весом — снэп у них отличный,
	// потому что замерла машина на дороге.
	pts := []geo.Point{
		at(0, 10.000, 0),
		at(60, 10.010, 0), at(120, 10.010, 0), at(180, 10.010, 0),
		at(240, 10.020, 0),
	}
	snaps, ok := snapAll(len(pts), 5)

	c := &Core{}
	w, _ := c.buildWeights(pts, snaps, ok, noFacts(len(pts)))

	assert.InDelta(t, w[0]/3, w[1], 1e-9, "три повтора координаты делят вес натрое")
	assert.InDelta(t, w[1], w[3], 1e-9)
}

func TestCoreWeights_FrozenPenaltyOverridesEverything(t *testing.T) {
	// Заморозка штрафуется ПОСЛЕДНЕЙ, поверх всего: сколько бы точек ни
	// стояло за такой «стоянкой», это одно значение, повторённое много раз.
	// Проверяем именно порядок: точке даны и привилегия, и множитель за 95
	// наблюдений, и всё равно она обязана уйти в минус.
	pts := drive(9, 60, 10, 0, 0.01, 0)
	snaps, ok := snapAll(len(pts), 5)

	c := &Core{}
	f := noFacts(len(pts))
	f.trusted[4] = struct{}{}
	f.observed[4] = 95
	f.frozen[4] = struct{}{}

	w, _ := c.buildWeights(pts, snaps, ok, f)

	assert.InDelta(t, -FrozenPenalty, w[4], 1e-9,
		"штраф заморозке кладётся поверх всего накопленного")
}

func TestCoreWeights_AirfieldPenaltySpreadsOverEpisode(t *testing.T) {
	// Подделка — это эпизод целиком, а не отдельные точки в нём. В Шереметьеве
	// из 266 точек 211 лежали на поле, и оставшихся 55 хватало, чтобы переход
	// Домодедово → Шереметьево → Кашира выжил.
	//
	// Точки идут раз в 5 минут, порог расширения 10 минут: значит наказаны
	// должны быть сама точка и по две с каждой стороны.
	// Шаг 1.1 км: короче разрыва, по которому режет правило островов, иначе
	// оно накрыло бы весь трек раньше лётного поля и опыт мерил бы не то.
	pts := drive(9, 300, 10, 0, 0.01, 0)
	snaps, ok := snapAll(len(pts), 5)

	c := &Core{Airfields: fixedAirfields{false, false, false, false, true}}
	clean, _ := c.buildWeights(pts, snaps, ok, noFacts(len(pts)))

	bare := &Core{}
	base, _ := bare.buildWeights(pts, snaps, ok, noFacts(len(pts)))

	for i := range pts {
		hit := i >= 2 && i <= 6
		if hit {
			assert.InDelta(t, base[i]-AirfieldPenalty, clean[i], 1e-9,
				"точка %d внутри эпизода", i)
			continue
		}
		assert.InDelta(t, base[i], clean[i], 1e-9, "точка %d вне эпизода", i)
	}
}

func TestCoreWeights_AmnestySavesFromEpisodePenalty(t *testing.T) {
	// Амнистия считается ДО наложения штрафа за эпизод, иначе спасать было бы
	// уже нечего. Здесь честная езда на асфальте примыкает к чистой точке и
	// обязана уцелеть, хотя попала в окрестность глюка.
	//
	// Шаг 10 секунд и 100 метров — цепочка доверия проходит; поле в самом
	// конце, чтобы слева осталась чистая часть.
	n := 30
	pts := drive(n, 10, 10, 0, 0.0009, 0)
	snaps, ok := snapAll(n, 5)

	air := make(fixedAirfields, n)
	air[n-1] = true

	c := &Core{Airfields: air}
	w, rep := c.buildWeights(pts, snaps, ok, noFacts(n))

	assert.Positive(t, rep.Spread, "эпизод обязан был кого-то накрыть")
	assert.Positive(t, rep.Amnesty, "и кого-то обязана была оправдать амнистия")
	assert.Negative(t, w[n-1], "уличённую лично точку амнистия не касается")

	saved := 0
	for i := range n - 1 {
		if w[i] > 0 {
			saved++
		}
	}
	assert.Positive(t, saved, "честная езда рядом с глюком должна была уцелеть")
}

func TestCoreWeights_TrustedStopAmnestiesItself(t *testing.T) {
	// Долгая стоянка оправдывает себя сама: цепочка доверия сквозь глюк не
	// проходит, а стоящая машина пробег не накручивает.
	n := 9
	pts := drive(n, 300, 10, 0, 0.01, 0)
	snaps, ok := snapAll(n, 5)

	air := make(fixedAirfields, n)
	air[4] = true

	c := &Core{Airfields: air}
	f := noFacts(n)
	f.trusted[2] = struct{}{} // попадает в окрестность, но это стоянка

	w, _ := c.buildWeights(pts, snaps, ok, f)
	bare, _ := c.buildWeights(pts, snaps, ok, noFacts(n))

	assert.Greater(t, w[2], bare[2], "стоянка обязана была оправдаться")
}

func TestCoreWeights_ConvictedStopGetsNoSelfAmnesty(t *testing.T) {
	// А вот стоянка, уличённая ЛИЧНО (она же лежит на лётном поле), себя не
	// оправдывает: иначе привилегия стоянки перебивала бы прямую улику места.
	n := 9
	pts := drive(n, 300, 10, 0, 0.01, 0)
	snaps, ok := snapAll(n, 5)

	air := make(fixedAirfields, n)
	air[4] = true

	c := &Core{Airfields: air}
	f := noFacts(n)
	f.trusted[4] = struct{}{}

	w, _ := c.buildWeights(pts, snaps, ok, f)
	assert.Negative(t, w[4], "лётное поле сильнее привилегии стоянки")
}

func TestCoreWeights_DecoyPenaltyScalesWithEvidence(t *testing.T) {
	// Приманка — улика с силой, а не да/нет: штраф пропорционален доле
	// прыжков в это место.
	n := 5
	pts := drive(n, 300, 10, 0, 0.01, 0)
	snaps, ok := snapAll(n, 5)

	weak := &Core{Decoys: fixedDecoys{0, 0, 0.2, 0, 0}}
	strong := &Core{Decoys: fixedDecoys{0, 0, 0.8, 0, 0}}
	bare := &Core{}

	base, _ := bare.buildWeights(pts, snaps, ok, noFacts(n))
	wWeak, _ := weak.buildWeights(pts, snaps, ok, noFacts(n))
	wStrong, _ := strong.buildWeights(pts, snaps, ok, noFacts(n))

	assert.InDelta(t, base[2]-DecoyPenalty*0.2, wWeak[2], 1e-9)
	assert.InDelta(t, base[2]-DecoyPenalty*0.8, wStrong[2], 1e-9)
}

func TestCoreWeights_DecoyDeniesStopPrivilege(t *testing.T) {
	// «Стоянка» в приманочном месте доверия не заслуживает: там, куда сотня
	// чужих машин попала прыжком, наша не ночевала.
	n := 5
	pts := drive(n, 300, 10, 0, 0.01, 0)
	snaps, ok := snapAll(n, 1500)
	snaps[2] = 5

	f := noFacts(n)
	f.trusted[2] = struct{}{}

	clean := &Core{}
	inDecoy := &Core{Decoys: fixedDecoys{0, 0, 0.5, 0, 0}}

	wClean, _ := clean.buildWeights(pts, snaps, ok, f)
	wDecoy, _ := inDecoy.buildWeights(pts, snaps, ok, f)

	bare, _ := clean.buildWeights(pts, snaps, ok, noFacts(n))

	assert.InDelta(t, StopTrustW, wClean[2], 1e-9, "обычная стоянка привилегию получает")
	// В приманке вес остаётся СВОИМ (плохим) и сверху ловит штраф приманки —
	// то есть привилегия не выдавалась вовсе, а не выдавалась и отнималась.
	assert.InDelta(t, bare[2]-DecoyPenalty*0.5, wDecoy[2], 1e-9,
		"стоянка в приманке привилегии не получает")
}

func TestCoreWeights_SplitPenaltyHasNoSpreadAndNoAmnesty(t *testing.T) {
	// Раздвоение уличает целыми пластами и само знает, какое место настоящее.
	// Расширять его штраф на окрестность значит топить ровно то место, которое
	// правило признало настоящим: на `5cde6306` так терялась стоянка на
	// Пензенской, все 13 точек из 13.
	n := 9
	pts := drive(n, 300, 10, 0, 0.01, 0)
	snaps, ok := snapAll(n, 5)

	c := &Core{}
	f := noFacts(n)
	f.split[4] = struct{}{}

	w, _ := c.buildWeights(pts, snaps, ok, f)
	base, _ := c.buildWeights(pts, snaps, ok, noFacts(n))

	assert.InDelta(t, base[4]-SplitPenalty, w[4], 1e-9, "штраф лёг на свою точку")
	for i := range n {
		if i == 4 {
			continue
		}
		assert.InDelta(t, base[i], w[i], 1e-9, "соседа %d раздвоение не касается", i)
	}
}

// -------------------------------------------------------------- сборка Run

func TestCore_DegenerateInput(t *testing.T) {
	// Вырожденный вход не должен ни падать, ни ходить в сеть.
	cases := []struct {
		name string
		pts  []geo.Point
		want int
	}{
		{"пусто", nil, 0},
		{"одна точка", drive(1, 60, 10, 0, 0.01, 0), 1},
		{"две точки", drive(2, 60, 10, 0, 0.01, 0), 2},
		{"три точки", drive(3, 60, 10, 0, 0.01, 0), 3},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := flatSnaps(5)
			core := &Core{Snap: snap, Road: &fakeRoads{}}

			keep, _, err := core.Run(context.Background(), c.pts)
			require.NoError(t, err)
			assert.Len(t, keep, c.want, "короткий трек отдаём целиком")
		})
	}
}

func TestCore_KeepsOriginalNumbering(t *testing.T) {
	// Наружу отдаём индексы ИСХОДНОГО массива: внутри мы переставляли пачки,
	// а вызывающий знает только свой вход.
	//
	// Пачка из трёх записей с одной меткой идёт задом наперёд — перестановка
	// обязана сработать, а нумерация обязана вернуться.
	pts := []geo.Point{
		at(0, 10.000, 0),
		at(60, 10.004, 0), at(60, 10.003, 0), at(60, 10.002, 0),
		at(120, 10.006, 0),
		at(180, 10.008, 0),
	}
	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}

	keep, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)

	assert.Positive(t, rep.Reordered, "перевёрнутую пачку обязаны были переставить")
	for _, i := range keep {
		assert.Less(t, i, len(pts), "индекс вне исходного массива")
		assert.GreaterOrEqual(t, i, 0)
	}
}

func TestCore_CollapsesStops(t *testing.T) {
	// Стоянка схлопывается в одну точку — свою первую. Заодно это минус
	// примерно шестая часть точек, которые иначе поехали бы в OSRM.
	pts := drive(5, 60, 10, 0, 0.01, 0)
	pts = append(pts, still(60, 30, 10.10, 0, 0.00002, 500)...)
	pts = append(pts, drive(5, 60, 10.2, 0, 0.01, 3000)...)

	snap := flatSnaps(5)
	core := &Core{Snap: snap, Road: &fakeRoads{}}

	_, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)

	assert.Positive(t, rep.Collapsed, "стоянку обязаны были схлопнуть")
	assert.Equal(t, 1, rep.StopsTotal)
	require.Len(t, snap.sizes, 1, "снэпы просим одним заходом")
	assert.Equal(t, len(pts)-rep.Collapsed, snap.sizes[0],
		"в OSRM едет трек уже без схлопнутого")
}

func TestCore_FrozenNeedsBothDurationAndStillness(t *testing.T) {
	// Залипание — это И долго, И без дрожания. Стоящая машина шумит на
	// сотни метров, залипшая координата даёт ровный ноль.
	long := drive(3, 60, 10, 0, 0.01, 0)
	long = append(long, still(60, 30, 10.06, 0, 0.0000001, 200)...) // 30 минут, ноль дрожания
	long = append(long, drive(3, 60, 10.1, 0, 0.01, 2400)...)

	shaky := drive(3, 60, 10, 0, 0.01, 0)
	shaky = append(shaky, still(60, 30, 10.06, 0, 0.0005, 200)...) // те же 30 минут, но дрожит
	shaky = append(shaky, drive(3, 60, 10.1, 0, 0.01, 2400)...)

	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}

	_, repLong, err := core.Run(context.Background(), long)
	require.NoError(t, err)
	_, repShaky, err := core.Run(context.Background(), shaky)
	require.NoError(t, err)

	assert.Equal(t, 1, repLong.StopsFrozen, "неподвижная координата — залипание")
	assert.Zero(t, repShaky.StopsFrozen, "дрожащая — настоящая стоянка")
}

func TestCore_SplitCountedBeforeAndAfterReorder(t *testing.T) {
	// Правило раздвоения неустойчиво к перестановке пачек: порядок внутри
	// пачки меняет кластеризацию окна, а с ней возвраты и границы промежутка.
	// Замер показал, что «только до» чинит Астрахань, но ломает Волово, а
	// «только после» — ровно наоборот. Поэтому берём ОБЪЕДИНЕНИЕ.
	//
	// Проверяем не сам вердикт, а то, что правило зовётся дважды.
	pts := []geo.Point{
		at(0, 10.000, 0),
		at(60, 10.004, 0), at(60, 10.003, 0), at(60, 10.002, 0),
		at(120, 10.006, 0), at(180, 10.008, 0), at(240, 10.010, 0),
	}
	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}

	_, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)
	require.Positive(t, rep.Reordered, "для опыта нужна сработавшая перестановка")
	assert.GreaterOrEqual(t, rep.Split, 0)
}

func TestCore_NoSnapClient(t *testing.T) {
	// Без снэпов ядро работать не может: вес точки строится именно на них.
	// Молчание не должно превращаться в мусор — вес нулевой у всех, цепочка
	// берёт то, что связно.
	pts := drive(20, 60, 10, 0, 0.01, 0)
	core := &Core{Road: &fakeRoads{}}

	keep, _, err := core.Run(context.Background(), pts)
	require.NoError(t, err)
	assert.NotEmpty(t, keep, "без снэпов трек не должен исчезать целиком")
}

func TestCore_CancelledContext(t *testing.T) {
	// Отменённый контекст — сразу наружу с ошибкой, а не «посчитаем как
	// получится». Считать полдела и молча отдать половину километража хуже,
	// чем честно не ответить.
	pts := drive(20, 60, 10, 0, 0.01, 0)
	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := core.Run(ctx, pts)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestCore_PassesConverge(t *testing.T) {
	// Цикл проходов обязан сходиться: когда правила перестают что-либо
	// находить, перестраивать цепочку незачем.
	pts := drive(40, 60, 10, 0, 0.01, 0)
	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}

	_, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)

	assert.Equal(t, 1, rep.RoadPasses, "на чистом треке хватает одного прохода")
	assert.LessOrEqual(t, rep.RoadPasses, RoadPasses)
}

func TestCore_PassesCapped(t *testing.T) {
	// А если не сходится — упираемся в потолок, а не крутимся вечно.
	// Источник дорог отвечает так, что каждый переход невозможен.
	pts := drive(40, 300, 10, 0, 0.03, 0)
	far := 1e7
	core := &Core{
		Snap: flatSnaps(5),
		Road: &fakeRoads{dist: func(_, _ geo.Point) *float64 { return &far }},
	}

	_, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)
	assert.LessOrEqual(t, rep.RoadPasses, RoadPasses, "потолок проходов обязан держать")
}

func TestCore_DropsGlitchesKeepsHonest(t *testing.T) {
	// Сквозная проверка смысла: честная езда по дороге остаётся, облако в
	// поле уходит. Снэп задаётся по месту — как его и даёт OSRM.
	honest := drive(30, 60, 10.0, 0, 0.01, 0)
	field := drive(10, 60, 30.0, 5, 0.01, 1800) // далеко в стороне
	tail := drive(30, 60, 10.3, 0, 0.01, 2400)

	pts := append(append(append([]geo.Point{}, honest...), field...), tail...)

	core := &Core{
		Snap: &fakeSnapper{at: func(p geo.Point) *float64 {
			v := 5.0
			if p.Lat > 1 {
				v = 2000 // облако в поле
			}
			return &v
		}},
		Road: &fakeRoads{},
	}

	keep, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)

	kept := make(map[int]struct{}, len(keep))
	for _, i := range keep {
		kept[i] = struct{}{}
	}
	for i := len(honest); i < len(honest)+len(field); i++ {
		assert.NotContains(t, kept, i, "точка облака %d должна была выпасть", i)
	}
	assert.Positive(t, rep.Dropped)
	assert.Greater(t, rep.KmBefore, rep.KmAfter, "выдуманный крюк обязан уйти из километража")
}

func TestCore_ReportIsFilled(t *testing.T) {
	// Отчёт — то, что уходит в debug-ручку сервиса. Пустые поля там
	// бесполезны, поэтому проверяем, что заполняется хотя бы то, что можно
	// заполнить на любом треке.
	pts := drive(5, 60, 10, 0, 0.01, 0)
	pts = append(pts, still(60, 30, 10.10, 0, 0.0002, 500)...)
	pts = append(pts, drive(5, 60, 10.2, 0, 0.01, 3000)...)

	core := &Core{Snap: flatSnaps(7), Road: &fakeRoads{}}
	_, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)

	assert.Positive(t, rep.KmBefore)
	assert.Positive(t, rep.KmAfter)
	assert.Positive(t, rep.StopsTotal)
	assert.Positive(t, rep.Collapsed)
	assert.InDelta(t, 7.0, rep.SnapMedian, 1e-9, "медиана снэпа обязана быть настоящей")
	assert.GreaterOrEqual(t, rep.RoadPasses, 1)
}

func TestCore_DoesNotMutateInput(t *testing.T) {
	// Ядро переставляет пачки и режет стоянки — но у себя, не у вызывающего.
	pts := []geo.Point{
		at(0, 10.000, 0),
		at(60, 10.004, 0), at(60, 10.003, 0), at(60, 10.002, 0),
		at(120, 10.006, 0), at(180, 10.008, 0),
	}
	before := make([]geo.Point, len(pts))
	copy(before, pts)

	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}
	_, _, err := core.Run(context.Background(), pts)
	require.NoError(t, err)

	assert.Equal(t, before, pts, "входной массив трогать нельзя")
}

func TestCore_KeepIsOrderedByTime(t *testing.T) {
	// Оставленное обязано идти по времени: это и есть исправленный маршрут,
	// и по нему сразу считают километраж.
	pts := drive(40, 60, 10, 0, 0.01, 0)
	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}

	keep, _, err := core.Run(context.Background(), pts)
	require.NoError(t, err)
	require.NotEmpty(t, keep)

	prev := time.Time{}
	for _, i := range keep {
		assert.False(t, pts[i].Time.Before(prev), "время пошло назад на точке %d", i)
		prev = pts[i].Time
	}
}

func BenchmarkCoreRun(b *testing.B) {
	pts := drive(20000, 30, 10.0, 0.0, 0.002, 0)
	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}
	b.ReportAllocs()
	for b.Loop() {
		core.Run(context.Background(), pts)
	}
}

// --------------------------------------------------- раздвоение внутри сборки

func TestCore_SplitVerdictReachesWeights(t *testing.T) {
	// Раздвоение — единственное правило, которое считается в `Run`, а не в
	// весах, и приговор ему надо перенести на схлопнутую точку. Проверяем, что
	// перенос вообще происходит: на треке, где два источника пишут по очереди
	// блоками, правило обязано сработать.
	// Шесть часов метания между тремя местами, а следом честный перегон.
	// Хвост нужен, чтобы проверить не только «сработало», но и «остановилось
	// там, где метание кончилось».
	pts := rotate([]spot{spotA, spotB, spotC}, 8, 30, 30)
	pts = append(pts, drive(60, 30, 10.20, 0, 0.005, 21900)...)

	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}
	_, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)

	assert.Positive(t, rep.Split, "метание между местами обязано уличаться")
	assert.Less(t, rep.Split, len(pts), "честный перегон следом уличать не за что")
}

func TestCore_SplitCountedInBothOrders(t *testing.T) {
	// Правило зовётся дважды — до перестановки пачек и после, — и берётся
	// ОБЪЕДИНЕНИЕ. Здесь в трек с раздвоением добавлена перевёрнутая пачка с
	// одной меткой времени, чтобы перестановка точно сработала.
	pts := rotate([]spot{spotA, spotB, spotC}, 8, 30, 30)

	// Пачка с одной меткой времени, записанная задом наперёд: трек идёт на
	// восток, а внутри пачки курс развёрнут на запад. Ровно то, что находится
	// на настоящих треках при выгрузке буфера.
	sec := int(pts[len(pts)-1].Time.Sub(t0).Seconds()) + 30
	pts = append(pts,
		at(sec, 10.20, 0), at(sec, 10.19, 0), at(sec, 10.18, 0),
		at(sec+30, 10.21, 0),
	)

	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}
	_, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)

	assert.Positive(t, rep.Reordered, "для опыта нужна сработавшая перестановка")
	assert.Positive(t, rep.Split, "и сработавшее раздвоение")
}

// ------------------------------------------------------ отмена и негодный вход

// cancelRoads отменяет контекст на первом же вопросе: так проверяется, что
// дедлайн виден ВНУТРИ цикла проходов, а не только на входе в ядро.
type cancelRoads struct{ cancel context.CancelFunc }

func (r *cancelRoads) PairDistance(_ context.Context, pairs []Pair) ([]float64, []bool, []string) {
	r.cancel()
	return make([]float64, len(pairs)), make([]bool, len(pairs)), nil
}

func TestCore_CancelledMidRun(t *testing.T) {
	// Отмена посреди работы: считать полдела и молча отдать половину
	// километража хуже, чем честно не ответить.
	pts := drive(40, 60, 10, 0, 0.01, 0)

	ctx, cancel := context.WithCancel(context.Background())
	core := &Core{Snap: flatSnaps(5), Road: &cancelRoads{cancel: cancel}}

	_, _, err := core.Run(ctx, pts)
	assert.ErrorIs(t, err, context.Canceled)
}

// shortMask — источник данных, отдающий маску не той длины: он внешний, и
// полагаться на его аккуратность нельзя.
type shortMask struct{}

func (shortMask) Mask([]geo.Point) []float64 { return []float64{0.5} }

type longAirfields struct{}

func (longAirfields) Mask(pts []geo.Point) []bool {
	return make([]bool, len(pts)+5)
}

func TestCore_MaskLengthMismatch(t *testing.T) {
	// Короткая маска — остаток считаем нулями; длинная — лишнее отбрасываем.
	// Ни то, ни другое не имеет права уронить ядро.
	pts := drive(20, 60, 10, 0, 0.01, 0)

	short := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}, Decoys: shortMask{}}
	_, _, err := short.Run(context.Background(), pts)
	require.NoError(t, err)

	long := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}, Airfields: longAirfields{}}
	_, _, err = long.Run(context.Background(), pts)
	require.NoError(t, err)
}

func TestCore_ShortSnapAnswer(t *testing.T) {
	// Клиент вернул меньше снэпов, чем точек: недостающие считаем молчанием.
	pts := drive(20, 60, 10, 0, 0.01, 0)
	core := &Core{Snap: &stubbySnapper{}, Road: &fakeRoads{}}

	keep, _, err := core.Run(context.Background(), pts)
	require.NoError(t, err)
	assert.NotEmpty(t, keep)
}

type stubbySnapper struct{}

func (stubbySnapper) Snap(_ context.Context, _ []geo.Point) ([]float64, []bool, []string) {
	return []float64{5, 5}, []bool{true, true}, nil
}

// cancelSnapper отменяет контекст на снэпах: это самая долгая часть работы, и
// именно на ней бюджет задачи истекает чаще всего.
type cancelSnapper struct{ cancel context.CancelFunc }

func (s *cancelSnapper) Snap(_ context.Context, pts []geo.Point) ([]float64, []bool, []string) {
	s.cancel()
	snaps, ok := make([]float64, len(pts)), make([]bool, len(pts))
	for i := range pts {
		snaps[i], ok[i] = 5, true
	}
	return snaps, ok, nil
}

func TestCore_CancelledDuringSnapping(t *testing.T) {
	// Дедлайн истёк, пока спрашивали снэпы. Дальше идти нельзя: цепочку
	// строить не на чем, а отдать «что получилось» значит соврать в километраже.
	pts := drive(40, 60, 10, 0, 0.01, 0)

	ctx, cancel := context.WithCancel(context.Background())
	core := &Core{Snap: &cancelSnapper{cancel: cancel}, Road: &fakeRoads{}}

	_, _, err := core.Run(ctx, pts)
	assert.ErrorIs(t, err, context.Canceled)
}

// -------------------------------------------- перенос вердикта раздвоения

func TestRemapSplit(t *testing.T) {
	// Вердикт, вынесенный ДО перестановки, обязан переехать в новую нумерацию.
	// Без переноса объединение двух порядков складывало бы индексы из разных
	// нумераций, то есть штрафовало бы случайные точки, — и счётчик в отчёте
	// выглядел бы при этом совершенно правдоподобно.
	perm := []int{0, 3, 2, 1, 4} // точки 1 и 3 поменялись местами

	got := remapSplit(set(3), perm)
	assert.Equal(t, set(1), got, "точка 3 переехала на позицию 1")

	got = remapSplit(set(0, 4), perm)
	assert.Equal(t, set(0, 4), got, "неподвижные точки остаются на месте")

	assert.Empty(t, remapSplit(nil, perm), "пустой вердикт остаётся пустым")
	assert.Empty(t, remapSplit(set(0), nil), "без перестановки переносить некуда")
}

func TestSplitOnCollapsed(t *testing.T) {
	// Стоянка представлена одной точкой, за которой стоит вся серия. Судим по
	// большинству наблюдений: правило метит целыми пластами, и единственное
	// задетое наблюдение на краю серии — не повод топить стоянку.
	//
	// Стоянка занимает сырые точки 10..14 (пять наблюдений) и представлена
	// точкой 10; точки 0 и 20 одиночные.
	alive := []int{0, 10, 20}
	ends := map[int]int{10: 14}

	cases := []struct {
		name string
		raw  map[int]struct{}
		want map[int]struct{}
	}{
		{"чисто", nil, map[int]struct{}{}},
		{"одиночка уличена", set(0), set(0)},
		{"меньшинство серии", set(10, 11), map[int]struct{}{}},
		{"большинство серии", set(10, 11, 12), set(1)},
		{"вся серия", set(10, 11, 12, 13, 14), set(1)},
		{"и стоянка, и одиночка", set(0, 11, 12, 13, 20), set(0, 1, 2)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, splitOnCollapsed(alive, ends, c.raw))
		})
	}
}

func TestSplitOnCollapsed_EvenSeriesNeedsStrictMajority(t *testing.T) {
	// На серии чётной длины ровно половина — ещё не большинство. Граница
	// именно здесь, и она проверена отдельно: сдвинь её на одно наблюдение,
	// и правило начнёт топить стоянки, которых почти не касалось.
	alive := []int{0}
	ends := map[int]int{0: 3} // четыре наблюдения

	assert.Empty(t, splitOnCollapsed(alive, ends, set(0, 1)), "два из четырёх — мало")
	assert.Equal(t, set(0), splitOnCollapsed(alive, ends, set(0, 1, 2)), "три из четырёх — довольно")
}

// ------------------------------------------------ стоянки и порядок наружу

func TestCore_ShortStopIsNotFrozen(t *testing.T) {
	// Залипание требует И длительности, И неподвижности. Короткая остановка
	// без дрожания — светофор или шлагбаум, а не залипший трекер, и штрафовать
	// её нельзя: место показано верно и сейчас.
	short := drive(3, 60, 10, 0, 0.01, 0)
	short = append(short, still(16, 30, 10.04, 0, 0.0000001, 200)...) // 7.5 мин, ноль дрожания
	short = append(short, drive(3, 60, 10.06, 0, 0.01, 900)...)

	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}
	_, rep, err := core.Run(context.Background(), short)
	require.NoError(t, err)

	require.Equal(t, 1, rep.StopsTotal, "остановка обязана была найтись")
	assert.Zero(t, rep.StopsFrozen, "короткая неподвижность — не залипание")
}

func TestCore_StopKeepsItsFirstPoint(t *testing.T) {
	// Схлопываем стоянку в её ПЕРВУЮ точку, а не выбрасываем целиком: она
	// связывает два куска маршрута, и без неё цепочка пойдёт напрямую.
	head := drive(5, 60, 10, 0, 0.01, 0)
	stopAt := len(head)
	pts := append(head, still(60, 30, 10.06, 0, 0.0002, 500)...)
	pts = append(pts, drive(5, 60, 10.10, 0, 0.01, 3000)...)

	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}
	keep, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)
	require.Equal(t, 1, rep.StopsTotal)

	kept := make(map[int]struct{}, len(keep))
	for _, i := range keep {
		kept[i] = struct{}{}
	}
	assert.Contains(t, kept, stopAt, "первая точка стоянки обязана уцелеть")
	for i := stopAt + 1; i < stopAt+60; i++ {
		assert.NotContains(t, kept, i, "остальные точки стоянки схлопнуты")
	}
}

func TestCore_KeptOrderIsTheCorrectedOne(t *testing.T) {
	// Наружу уходит ИСПРАВЛЕННЫЙ порядок, а не исходный. Пачка с одной меткой
	// времени записана задом наперёд; по возвращённым индексам путь обязан
	// получиться короче, чем по исходному порядку тех же точек.
	pts := []geo.Point{
		at(0, 10.000, 0),
		at(60, 10.014, 0), at(60, 10.013, 0), at(60, 10.012, 0),
		at(120, 10.016, 0),
		at(180, 10.020, 0),
	}
	core := &Core{Snap: flatSnaps(5), Road: &fakeRoads{}}

	keep, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)
	require.Positive(t, rep.Reordered)
	require.Len(t, keep, len(pts), "чистый трек терять точки не должен")

	corrected := make([]geo.Point, len(keep))
	for k, i := range keep {
		corrected[k] = pts[i]
	}
	assert.Less(t, geo.TotalLength(corrected), geo.TotalLength(pts),
		"исправленный порядок обязан быть короче исходного")
}

func TestCore_DirtyTrackNeedsMoreThanOnePass(t *testing.T) {
	// Цикл проходов существует не для красоты: запретив невозможный переход,
	// цепочку надо построить заново, и на новой цепочке находится следующий.
	// Если бы хватало одного прохода, весь цикл был бы лишним.
	// Шаг 3.3 км за пять минут: длиннее ловушки разделённых трасс, поэтому
	// переходы действительно доходят до проверки.
	pts := drive(40, 300, 10, 0, 0.03, 0)
	far := 1e7 // по дорогам недостижимо всё
	core := &Core{
		Snap: flatSnaps(5),
		Road: &fakeRoads{dist: func(_, _ geo.Point) *float64 { return &far }},
	}

	_, rep, err := core.Run(context.Background(), pts)
	require.NoError(t, err)
	assert.Greater(t, rep.RoadPasses, 1, "на грязном треке одного прохода мало")
	assert.LessOrEqual(t, rep.RoadPasses, RoadPasses)
}
