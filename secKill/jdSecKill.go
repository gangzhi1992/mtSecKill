package secKill

import (
	"context"
	"errors"
	"fmt"
	"github.com/axgle/mahonia"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/tidwall/gjson"
	"github.com/zqijzqj/mtSecKill/chromedpEngine"
	"github.com/zqijzqj/mtSecKill/global"
	"github.com/zqijzqj/mtSecKill/logs"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type jdSecKill struct {
	ctx context.Context
	cancel context.CancelFunc
	bCtx context.Context
	bWorksCtx []context.Context
	isLogin bool
	isClose bool
	mu sync.Mutex
	userAgent string
	UserInfo gjson.Result
	SkuId string
	SecKillUrl string
	SecKillNum int
	SecKillInfo gjson.Result
	eid string
	fp string
	Works int
	IsOkChan chan struct{}
	IsOk bool
	StartTime time.Time
	DiffTime int64
}

func NewJdSecKill(execPath string, skuId string, num, works int) *jdSecKill {
	if works < 0 {
		works = 1
	}
	jsk :=  &jdSecKill{
		ctx:    nil,
		isLogin: false,
		isClose: false,
		userAgent:chromedpEngine.GetRandUserAgent(),
		SkuId:skuId,
		SecKillNum:num,
		Works:works,
		IsOk:false,
		IsOkChan:make(chan struct{}, 1),
	}
	jsk.ctx, jsk.cancel = chromedpEngine.NewExecCtx(chromedp.ExecPath(execPath), chromedp.UserAgent(jsk.userAgent))
	return jsk
}

func (jsk *jdSecKill) Stop() {
	jsk.mu.Lock()
	defer jsk.mu.Unlock()
	if jsk.isClose {
		return
	}
	jsk.isClose = true
	c := jsk.cancel
	c()
	return
}

func (jsk *jdSecKill) GetReq(reqUrl string, params map[string]string, referer string, ctx context.Context) (gjson.Result, error) {
	if referer == "" {
		referer = "https://www.jd.com"
	}
	if ctx == nil {
		ctx = jsk.bCtx
	}
	req, _ := http.NewRequest("GET", reqUrl, nil)
	req.Header.Add("User-Agent", jsk.userAgent)
	req.Header.Add("Referer", referer)
	req.Header.Add("Host", req.URL.Host)
	if len(params) > 0 {
		q := req.URL.Query()
		for k, v := range params {
			q.Add(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
	resp, err := chromedpEngine.RequestByCookie(ctx, req)
	if err != nil {
		return gjson.Result{}, err
	}
	defer resp.Body.Close()
	b, _ := ioutil.ReadAll(resp.Body)
	logs.PrintlnSuccess("Get请求接口:", req.URL)
//	logs.PrintlnSuccess(string(b))
	logs.PrintlnInfo("=======================")
	r := FormatJdResponse(b, req.URL.String(), false)
	if r.Raw == "null" || r.Raw == "" {
		return gjson.Result{}, errors.New("获取数据失败：" + r.Raw)
	}
	return r, nil
}

func (jsk *jdSecKill) SyncJdTime() {
	resp, _ := http.Get("https://a.jd.com//ajax/queryServerData.html")
	defer resp.Body.Close()
	b, _ := ioutil.ReadAll(resp.Body)
	r := gjson.ParseBytes(b)
	jdTimeUnix := r.Get("serverTime").Int()
	jsk.DiffTime = global.UnixMilli() - jdTimeUnix
	logs.PrintlnInfo("服务器与本地时间差为: ", jsk.DiffTime, "ms")
}

func (jsk *jdSecKill) PostReq(reqUrl string, params url.Values, referer string, ctx context.Context) (gjson.Result, error) {
	if ctx == nil {
		ctx = jsk.bCtx
	}
	req, _ := http.NewRequest("POST", reqUrl, strings.NewReader(params.Encode()))
	req.Header.Add("User-Agent", jsk.userAgent)
	if referer != "" {
		req.Header.Add("Referer", referer)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add("Host", req.URL.Host)
	resp, err := chromedpEngine.RequestByCookie(ctx, req)
	if err != nil {
		return gjson.Result{}, err
	}
	defer resp.Body.Close()
	b, _ := ioutil.ReadAll(resp.Body)

	logs.PrintlnSuccess("Post请求连接", req.URL)
	logs.PrintlnInfo("=======================")
	r := FormatJdResponse(b, req.URL.String(), false)
	if r.Raw == "null" || r.Raw == "" {
		return gjson.Result{}, errors.New("获取数据失败：" + r.Raw)
	}
	return r, nil
}

func FormatJdResponse(b []byte, prefix string, isConvertStr bool) gjson.Result {
	r := string(b)
	if isConvertStr {
		r = mahonia.NewDecoder("gbk").ConvertString(r)
	}
	r = strings.TrimSpace(r)
	if prefix != "" {
		//这里针对http连接 自动提取jsonp的callback
		if strings.HasPrefix(prefix, "http") {
			pUrl, err := url.Parse(prefix)
			if err == nil {
				prefix = pUrl.Query().Get("callback")
			}
		}
		r = strings.TrimPrefix(r, prefix)
	}
	if strings.HasSuffix(r, ")") {
		r = strings.TrimLeft(r, `(`)
		r = strings.TrimRight(r, ")")
	}
	return gjson.Parse(r)
}

//初始化监听请求数据
func (jsk *jdSecKill) InitActionFunc() chromedp.ActionFunc {
	return func(ctx context.Context) error {
		jsk.bCtx = ctx
		_ = network.Enable().Do(ctx)
		chromedp.ListenTarget(ctx, func(ev interface{}) {
			switch e := ev.(type) {
			case *network.EventResponseReceived:
				go func() {
					if strings.Contains(e.Response.URL, "passport.jd.com/user/petName/getUserInfoForMiniJd.action") {
						b, err := network.GetResponseBody(e.RequestID).Do(ctx)
						if err == nil {
							jsk.UserInfo = FormatJdResponse(b, e.Response.URL, false)
						}
						jsk.isLogin = true
					}
				}()

			}
		})
		return nil
	}
}

func (jsk *jdSecKill) Run() error {
	return chromedp.Run(jsk.ctx, chromedp.Tasks{
		jsk.InitActionFunc(),
		chromedp.Navigate("https://passport.jd.com/uc/login"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			logs.PrintlnInfo("等待登陆......")
			for {
				if jsk.isLogin {
					logs.PrintlnSuccess(jsk.UserInfo.Get("realName").String() + ", 登陆成功........")
					break
				}
			}
			return nil
		}),
		jsk.GetEidAndFp(),
		jsk.WaitStart(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			//提取抢购连接
			for _, v := range jsk.bWorksCtx {
				go func(ctx2 context.Context) {
					jsk.FetchSecKillUrl()
					logs.PrintlnInfo("正在访问抢购连接......")
					_, _, _, _  = page.Navigate(jsk.SecKillUrl).WithReferrer("https://item.jd.com/"+jsk.SkuId+".html").Do(ctx2)
					SecKillRE:
					//请求抢购连接，提交订单
					err := jsk.ReqSubmitSecKillOrder(ctx2)
					if err != nil {
						logs.PrintlnInfo(err, "等待重试")
						goto SecKillRE
					}
				}(v)
			}
			select {
				case <-jsk.IsOkChan:
					logs.PrintlnInfo("抢购成功。。。10s后关闭进程...")
					_ = chromedp.Sleep(10 * time.Second).Do(ctx)
				case <-jsk.ctx.Done():
				case <-jsk.bCtx.Done():
			}
			return nil
		}),
	})
}

func (jsk *jdSecKill) WaitStart() chromedp.ActionFunc {
	return func(ctx context.Context) error {
		u := "https://item.jd.com/"+jsk.SkuId+".html"
		for i := 0; i < jsk.Works; i++ {
			go func() {
				tid, err := target.CreateTarget(u).Do(ctx)
				if err == nil {
					c, _ := chromedp.NewContext(jsk.bCtx, chromedp.WithTargetID(tid))
					_ = chromedp.Run(c, chromedp.Tasks{
						chromedp.ActionFunc(func(ctx context.Context) error {
							logs.PrintlnInfo("打开新的抢购标签.....")
							jsk.mu.Lock()
							jsk.bWorksCtx = append(jsk.bWorksCtx, ctx)
							jsk.mu.Unlock()
							return nil
						}),
					})
				}
			}()
		}
		_ = chromedp.Navigate(u).Do(ctx)
		st := jsk.StartTime.UnixNano() / 1e6
		logs.PrintlnInfo("等待时间到达"+jsk.StartTime.Format(global.DateTimeFormatStr)+"...... 请勿关闭浏览器")
		for {
			if global.UnixMilli() - jsk.DiffTime >= st {
				logs.PrintlnInfo("时间到达。。。。开始执行")
				break
			}
		}
		return nil
	}
}

func (jsk *jdSecKill) GetEidAndFp() chromedp.ActionFunc {
	return func(ctx context.Context) error {
		RE:
		logs.PrintlnInfo("正在获取eid和fp参数....")
		_ = chromedp.Navigate("https://search.jd.com/Search?keyword=乌江榨菜").Do(ctx)
		logs.PrintlnInfo("等待页面更新完成....")
		_ = chromedp.WaitVisible(".gl-item").Do(ctx)
		var itemNodes []*cdp.Node
		err := chromedp.Nodes(".gl-item", &itemNodes, chromedp.ByQueryAll).Do(ctx)
		if err != nil {
			return err
		}
		n := itemNodes[rand.Intn(len(itemNodes))]
		_ = dom.ScrollIntoViewIfNeeded().WithNodeID(n.NodeID).Do(ctx)
		_, _, _, _ = page.Navigate("https://item.jd.com/"+n.AttributeValue("data-sku")+".html").Do(ctx)

		logs.PrintlnInfo("等待商品详情页更新完成....")
		_ = chromedp.WaitVisible("#InitCartUrl").Do(ctx)
		_ = chromedp.Sleep(1 * time.Second).Do(ctx);
		_ = chromedp.Click("#InitCartUrl").Do(ctx)
		_ = chromedp.WaitVisible("#GotoShoppingCart").Do(ctx)
		_ = chromedp.Sleep(1 * time.Second).Do(ctx);
		_ = chromedp.Click("#GotoShoppingCart").Do(ctx)
		//_ = chromedp.Navigate("https://cart.jd.com/cart_index/").Do(ctx)
		_ = chromedp.WaitVisible("#cart-body").Do(ctx)
		_ = chromedp.Click(".common-submit-btn").Do(ctx)
		//_ = chromedp.WaitVisible("#mainframe").Do(ctx)
		ch, cc := chromedpEngine.WaitDocumentUpdated(ctx)
		defer cc()
		logs.PrintlnInfo("等待结算页加载完成..... 如遇到未选中商品错误，可手动选中后点击结算")
		<-ch
		//执行js参数 将eid和fp显示到对应元素上
		_ = chromedp.Sleep(3 * time.Second).Do(ctx)
		res := make(map[string]interface{})
		err = chromedp.Evaluate("_JdTdudfp", &res).Do(ctx)
		logs.PrintErr(err)
		logs.Println("_JdTdudfp: ", res)
		eid, ok := res["eid"]
		if !ok {
			logs.PrintlnInfo("获取eid失败,正在重试")
			goto RE
		}
		jsk.eid = eid.(string)
		jsk.fp = res["fp"].(string)

		if jsk.fp == "" || jsk.eid == "" || jsk.fp == "undefined" || jsk.eid == "undefined" {
			logs.PrintlnWarning("获取参数失败，等待重试。。。 重试过程过久可手动刷新浏览器")
			goto RE
		}
		logs.PrintlnInfo("参数获取成功：eid【"+jsk.eid+"】, fp【"+jsk.fp+"】")

		return nil
	}
}

func (jsk *jdSecKill) FetchSecKillUrl() {
	logs.PrintlnInfo("开始获取抢购连接.....")
	for {
		if jsk.SecKillUrl != "" {
			break
		}
		jsk.SecKillUrl = jsk.GetSecKillUrl()
		logs.PrintlnWarning("抢购链接获取失败.....正在重试")
	}
	jsk.SecKillUrl = "https:" + strings.TrimPrefix(jsk.SecKillUrl, "https:")
	jsk.SecKillUrl = strings.ReplaceAll(jsk.SecKillUrl, "divide", "marathon")
	jsk.SecKillUrl = strings.ReplaceAll(jsk.SecKillUrl, "user_routing", "captcha.html")
	logs.PrintlnSuccess("抢购连接获取成功....", jsk.SecKillUrl)
	return
}

func (jsk *jdSecKill) ReqSubmitSecKillOrder(ctx context.Context) error {
	if ctx == nil {
		ctx = jsk.bCtx
	}
	//这里直接使用浏览器跳转 主要目的是获取cookie
	skUrl := fmt.Sprintf("https://marathon.jd.com/seckill/seckill.action?=skuId=%s&num=%d&rid=%d", jsk.SkuId, jsk.SecKillNum, time.Now().Unix())
	logs.PrintlnInfo("访问抢购订单结算页面......", skUrl)
	_, _, _, _ = page.Navigate(skUrl).WithReferrer("https://item.jd.com/"+jsk.SkuId+".html").Do(ctx)

	logs.PrintlnInfo("获取抢购信息...............")
	for {
		err := jsk.GetSecKillInitInfo(ctx)
		if err != nil {
			logs.PrintErr("抢购失败：", err, "正在重试.......")
		}
		break
	}
	orderData := jsk.GetOrderReqData()

	if len(orderData) == 0 {
		return errors.New("订单参数生成失败")
	}
	logs.PrintlnInfo("订单参数：", orderData.Encode())
	logs.PrintlnInfo("提交抢购订单.............")
	r, err := jsk.PostReq("https://marathon.jd.com/seckillnew/orderService/pc/submitOrder.action?skuId="+jsk.SkuId+"", orderData, skUrl, ctx)
	if err != nil {
		logs.PrintErr("抢购失败：", err)
	}
	orderId := r.Get("orderId").String()
	if orderId != "" && orderId != "0" {
		jsk.IsOk = true
		jsk.IsOkChan <- struct{}{}
		logs.PrintlnInfo("抢购成功，订单编号:", r.Get("orderId").String())
	} else {
		if r.IsObject() || r.IsArray() {
			return errors.New("抢购失败：" + r.Raw)
		}
		return errors.New("抢购失败,再接再厉")
	}
	return nil
}

func (jsk *jdSecKill) GetOrderReqData() url.Values {
	logs.PrintlnInfo("生成订单所需参数...")
	defer func() {
		if f := recover(); f != nil {
			logs.PrintErr("订单参数错误：", f)
		}
	}()

	addressList := jsk.SecKillInfo.Get("addressList").Array()
	var defaultAddress gjson.Result
	for _, dAddress := range addressList {
		if dAddress.Get("defaultAddress").Bool() {
			logs.PrintlnInfo("获取到默认收货地址")
			defaultAddress = dAddress
		}
	}
	if defaultAddress.Raw == "" {
		logs.PrintlnInfo("没有获取到默认收货地址， 自动选择一个地址")
		defaultAddress = addressList[0]
	}
	invoiceInfo := jsk.SecKillInfo.Get("invoiceInfo")
	r := url.Values{
		"skuId":[]string{jsk.SkuId},
		"num":[]string{strconv.Itoa(jsk.SecKillNum)},
		"addressId":[]string{defaultAddress.Get("id").String()},
		"yuShou":[]string{"true"},
		"isModifyAddress":[]string{"false"},
		"name":[]string{defaultAddress.Get("name").String()},
		"provinceId":[]string{defaultAddress.Get("provinceId").String()},
		"cityId":[]string{defaultAddress.Get("cityId").String()},
		"countyId":[]string{defaultAddress.Get("countyId").String()},
		"townId":[]string{defaultAddress.Get("townId").String()},
		"addressDetail":[]string{defaultAddress.Get("addressDetail").String()},
		"mobile":[]string{defaultAddress.Get("mobile").String()},
		"mobileKey":[]string{defaultAddress.Get("mobileKey").String()},
		"email":[]string{defaultAddress.Get("email").String()},
		"postCode":[]string{""},
		"invoiceTitle":[]string{""},
		"invoiceCompanyName":[]string{""},
		"invoiceContent":[]string{},
		"invoiceTaxpayerNO":[]string{""},
		"invoiceEmail":[]string{""},
		"invoicePhone":[]string{invoiceInfo.Get("invoicePhone").String()},
		"invoicePhoneKey":[]string{invoiceInfo.Get("invoicePhoneKey").String()},
		"invoice":[]string{"true"},
		"password":[]string{""},
		"codTimeType":[]string{"3"},
		"paymentType":[]string{"4"},
		"areaCode":[]string{""},
		"overseas":[]string{"0"},
		"phone":[]string{""},
		"eid":[]string{jsk.eid},
		"fp":[]string{jsk.fp},
		"token":[]string{jsk.SecKillInfo.Get("token").String()},
		"pru":[]string{""},

	}

	t := invoiceInfo.Get("invoiceTitle").String()
	if t != "" {
		r["invoiceTitle"] = []string{t}
	} else {
		r["invoiceTitle"] = []string{"-1"}
	}

	t = invoiceInfo.Get("invoiceContentType").String()
	if t != "" {
		r["invoiceContent"] = []string{t}
	} else {
		r["invoiceContent"] = []string{"1"}
	}

	return r
}

func (jsk *jdSecKill) GetSecKillInitInfo(ctx context.Context) error {
	r, err := jsk.PostReq("https://marathon.jd.com/seckillnew/orderService/pc/init.action", url.Values{
		"sku":[]string{jsk.SkuId},
		"num":[]string{strconv.Itoa(jsk.SecKillNum)},
		"isModifyAddress":[]string{"false"},
	}, fmt.Sprintf("https://marathon.jd.com/seckill/seckill.action?=skuId=100012043978&num=2&rid=%d6", time.Now().Unix()), ctx)
	if err != nil {
		return err
	}
	jsk.SecKillInfo = r
	logs.PrintlnInfo("秒杀信息获取成功：", jsk.SecKillInfo.Raw)
	return nil
}

func (jsk *jdSecKill) GetSecKillUrl() string {
	req, _ := http.NewRequest("GET", "https://itemko.jd.com/itemShowBtn", nil)
	req.Header.Add("User-Agent", jsk.userAgent)
	req.Header.Add("Referer", "https://item.jd.com/"+jsk.SkuId+".html")
	r, _ := jsk.GetReq("https://itemko.jd.com/itemShowBtn", map[string]string{
		"callback":"jQuery" + strconv.FormatInt(global.GenerateRangeNum(1000000, 9999999), 10),
		"skuId":jsk.SkuId,
		"from":"pc",
		"_": strconv.FormatInt(time.Now().Unix() * 1000, 10),
	}, "https://item.jd.com/"+jsk.SkuId+".html", nil)
	return r.Get("url").String()
}
